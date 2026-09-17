// Engine — the omp candidate chain, transient-error classification, and
// retry policy (agent-session.ts #resolveCompactionModelCandidates,
// #compactWithFallbackModel, and the retry loop at 9732-9824).
package compact

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RetryDefaults — omp's compaction retry settings (settings-schema:
// retry.maxRetries / baseDelayMs on the compaction path).
const (
	DefaultMaxRetries = 3
	DefaultBaseDelay  = 500 * time.Millisecond
	// maxAcceptableDelayMs — omp: when the next retry is further out than
	// this, try the next candidate instead of waiting (compaction.ts:9796).
	maxAcceptableDelay = 30 * time.Second
)

// CandidateChain builds the omp candidate order over evilcode's model refs:
// the session model first, then each configured role model, then the
// largest-context model available. Dedup by ref (agent-session.ts:9252).
func CandidateChain(sessionModel string, roleModels []string, available []ModelInfoLite) []ModelInfoLite {
	var out []ModelInfoLite
	seen := map[string]bool{}
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ModelInfoLite{Ref: ref})
	}
	add(sessionModel)
	for _, role := range roleModels {
		add(role)
	}
	// The largest-context available model is the last resort; an unknown
	// window (0) never wins the sort.
	if len(available) > 0 {
		best := available[0]
		for _, m := range available {
			if m.ContextWindow > best.ContextWindow {
				best = m
			}
		}
		if best.ContextWindow > 0 {
			add(best.Ref)
		}
	}
	// Attach context windows from the registry where the ref matches.
	for i := range out {
		for _, m := range available {
			if m.Ref == out[i].Ref {
				out[i].ContextWindow = m.ContextWindow
				break
			}
		}
	}
	return out
}

var (
	statusCodeRe = regexp.MustCompile(`\b(40[13]|429|5\d\d)\b`)
	retryAfterRe = regexp.MustCompile(`(?i)retry[-_ ]?after[: ]*(\d+)`)
	usageLimitRe = regexp.MustCompile(`(?i)usage limit|rate limit|quota|too many requests`)
	transientRe  = regexp.MustCompile(`(?i)timeout|temporar|connection reset|connection refused|unavailable|eof|broken pipe|502|503|504`)
	authRe       = regexp.MustCompile(`(?i)unauthorized|forbidden|invalid api key|authentication|no auth available|auth_unavailable`)
	overflowRe   = regexp.MustCompile(`(?i)context (length|window) exceed|too many tokens|maximum context|context length|input.*too (long|large)|exceeds.*context`)
)

// ClassifyError sorts a summarizer failure the way omp's retry loop does
// (compaction.ts:9781): transient errors retry the same model with backoff;
// auth failures move to the next candidate immediately; a usage-limit error
// honors Retry-After when the provider sends one.
type ErrorKind int

const (
	ErrTransient ErrorKind = iota
	ErrAuth
	ErrPermanent
	ErrContextOverflow
)

func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ErrPermanent
	}
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(err, context.Canceled) {
			return ErrPermanent // user interrupt is not a provider failure
		}
		return ErrTransient
	}
	if authRe.MatchString(msg) {
		return ErrAuth
	}
	if overflowRe.MatchString(msg) {
		return ErrContextOverflow
	}
	if usageLimitRe.MatchString(msg) {
		return ErrTransient
	}
	if transientRe.MatchString(msg) {
		return ErrTransient
	}
	if status := statusFrom(msg); status == 429 || status >= 500 {
		return ErrTransient
	}
	if status := statusFrom(msg); status == 401 || status == 403 {
		return ErrAuth
	}
	return ErrPermanent
}

func statusFrom(msg string) int {
	m := statusCodeRe.FindString(msg)
	if m == "" {
		return 0
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0
	}
	return n
}

// RetryAfter extracts a provider-advised delay in milliseconds; 0 when the
// response carried none (compaction.ts: retryAfterMs).
func RetryAfter(err error) int {
	if err == nil {
		return 0
	}
	m := retryAfterRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	secs, perr := strconv.Atoi(m[1])
	if perr != nil {
		return 0
	}
	return secs * 1000
}

// RunSummarizer walks the candidate chain with omp's retry policy: transient
// failures retry the same model with exponential backoff (bounded by
// Retry-After when present, and abandoned for the next candidate beyond
// maxAcceptableDelay); auth and permanent failures move on immediately
// (compaction.ts:9732-9824).
func RunSummarizer(ctx context.Context, summarize Summarizer, candidates []ModelInfoLite,
	run func(ctx context.Context, model ModelRef) (string, error),
) (string, ModelRef, error) {
	if len(candidates) == 0 {
		return "", "", errors.New("compaction failed: no available model")
	}
	var lastErr error
	for _, candidate := range candidates {
		attempt := 0
		for {
			out, err := run(ctx, candidate.Ref)
			if err == nil {
				return out, candidate.Ref, nil
			}
			lastErr = err
			switch ClassifyError(err) {
			case ErrTransient:
			case ErrAuth, ErrPermanent, ErrContextOverflow:
				// Next candidate. (An overflow during a compaction side-call
				// means this model cannot even hold the transcript; retrying
				// it would produce the same overflow.)
			}
			if ClassifyError(err) != ErrTransient {
				break
			}
			if attempt >= DefaultMaxRetries {
				break
			}
			delay := DefaultBaseDelay << attempt
			if ra := time.Duration(RetryAfter(err)) * time.Millisecond; ra > delay {
				delay = ra
				if delay > maxAcceptableDelay {
					break // too far out; try the next candidate
				}
			}
			attempt++
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no compaction model succeeded")
	}
	return "", "", lastErr
}

// isContextOverflowRe reports a provider error meaning the request exceeded
// the model's context window.
var isContextOverflowRe = regexp.MustCompile(`(?i)context (length|window)|too many tokens|maximum context|input.*too (long|large)|exceeds.*context|prompt is too long|reduce the length`)

// isLengthCapRe reports a provider error meaning the COMPLETION hit its
// length cap — the request fit; the answer was cut.
var isLengthCapRe = regexp.MustCompile(`(?i)length (limit|cap)|max_tokens|finish_reason.*length|incomplete`)

// IsContextOverflow reports whether err is a provider context-overflow.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	if ClassifyError(err) == ErrContextOverflow {
		return true
	}
	return isContextOverflowRe.MatchString(err.Error())
}

// IsLengthCap reports whether err is a completion length-cap.
func IsLengthCap(err error) bool {
	if err == nil {
		return false
	}
	return isLengthCapRe.MatchString(err.Error())
}

// sanitizeUser ensures the user prompt never carries a system role into a
// side-call that would treat it as instructions: evilcode's sideCallOnce
// already separates system and user; this only normalizes emptiness.
func normalizeUser(s string) string { return strings.TrimSpace(s) }

// CompactInfo mirrors session.CompactInfo without the session-package
// dependency: the adapter translates. ShortSummary and token accounting ride
// the compaction meta entry.
type CompactInfo struct {
	Summary      string
	ShortSummary string
	TokensBefore int
	KeptTokens   int
}
