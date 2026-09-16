// Context-reducing surgical compaction ("shake") — the port of omp's
// shake.ts. Pure detection (collectShakeRegions) plus pure mutation
// (applyShakeRegions); persistence and offload belong to the caller, which
// is the session rewrite and the artifacts dir here.
package compact

import (
	"fmt"
	"regexp"
	"strings"

	"evilcode/internal/provider"
)

// ShakeConfig — omp ShakeConfig (shake.ts:25). protectTokens keeps the most
// recent context intact; minSavings gates a shake to worthwhile runs.
type ShakeConfig struct {
	ProtectTokens  int
	MinSavings     int
	FenceMinTokens int
	// SkipBefore is the message index where the last compaction boundary
	// sits: entries before it are summarized away and never sent, so
	// shaking them only churns persisted history. -1 or 0 = whole slice.
	SkipBefore int
}

// DefaultShakeConfig — omp auto-shake: protect the live tail, conservative
// thresholds (shake.ts:45).
func DefaultShakeConfig() ShakeConfig {
	return ShakeConfig{ProtectTokens: 16000, MinSavings: 4000, FenceMinTokens: 400}
}

// AggressiveShakeConfig — omp manual `/shake`: drop every eligible region
// across history (shake.ts:52).
func AggressiveShakeConfig() ShakeConfig {
	return ShakeConfig{FenceMinTokens: 400}
}

// PLACEHOLDER_TOKEN_ESTIMATE is the rough token cost of a placeholder line;
// used only for the savings gate (shake.ts:61).
const PLACEHOLDER_TOKEN_ESTIMATE = 16

// ShakeRegion is one elidable stretch (shake.ts:64). ToolResult regions are
// whole results; Block regions are fenced code or top-level XML spans inside
// a message's text.
type ShakeRegion struct {
	// Kind: "toolResult" or "block".
	Kind string
	// Index of the message in the slice.
	MsgIndex int
	// Start/end are byte offsets into the target text for block regions.
	Start, End int
	// Tokens is the estimated size of the original stretch.
	Tokens int
	// Label is the human name for the offload doc: tool name, or role.
	Label string
	// Original is the exact text being replaced (for the offload doc).
	Original string
}

var (
	fenceRe    = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\n.*?\n```")
	xmlOpenRe  = regexp.MustCompile(`^<([a-z_-]+)(?:\s+[^>]*)?>$`)
	xmlCloseRe = regexp.MustCompile(`^</([a-z_-]+)>$`)
)

// scanTextForBlockRanges locates fenced code blocks and top-level XML
// element spans inside text (shake.ts:131). Conservative: lowercase XML tags
// only, whole-line matches.
func scanTextForBlockRanges(text string, minTokens int) []ShakeRegion {
	if countTokens(text) < minTokens {
		return nil
	}
	var out []ShakeRegion
	// Fenced blocks.
	for _, loc := range fenceRe.FindAllStringIndex(text, -1) {
		inner := text[loc[0]:loc[1]]
		if countTokens(inner) < minTokens {
			continue
		}
		out = append(out, ShakeRegion{Kind: "block", Start: loc[0], End: loc[1], Original: inner})
	}
	// Top-level XML spans: line-oriented scan; a lowercase opening tag with
	// depth 1 that closes at depth 0 marks a span.
	lines := strings.Split(text, "\n")
	depth := 0
	var startTag string
	var startOffset int
	offset := 0
	spanOpen := false
	for _, line := range lines {
		lineLen := len(line) + 1
		trimmed := strings.TrimSpace(line)
		if m := xmlOpenRe.FindStringSubmatch(trimmed); m != nil && !spanOpen {
			if depth == 0 {
				startTag = m[1]
				startOffset = offset
				spanOpen = true
			}
			depth++
		} else if m := xmlCloseRe.FindStringSubmatch(trimmed); m != nil && spanOpen {
			depth--
			if depth == 0 {
				span := text[startOffset : offset+len(line)]
				if countTokens(span) >= minTokens {
					out = append(out, ShakeRegion{Kind: "block", Start: startOffset, End: offset + len(line), Original: span})
				}
				spanOpen = false
			}
		}
		offset += lineLen
	}
	_ = startTag
	return out
}

// CollectShakeRegions locates every eligible region on a conversation
// (shake.ts:286). Tool results (whole) and blocks inside user/assistant text
// are eligible once the entries newer than them exceed protectTokens.
func CollectShakeRegions(msgs []provider.Message, config ShakeConfig) []ShakeRegion {
	n := len(msgs)
	if n == 0 {
		return nil
	}
	// Tokens of all entries strictly more recent than index i.
	accumulatedAfter := make([]int, n)
	acc := 0
	for i := n - 1; i >= 0; i-- {
		accumulatedAfter[i] = acc
		acc += estimateTokens(msgs[i])
	}

	start := config.SkipBefore
	if start < 0 || start > n {
		start = 0
	}

	var regions []ShakeRegion
	for i := start; i < n; i++ {
		msg := msgs[i]
		if msg.Role == provider.RoleTool {
			if accumulatedAfter[i] < config.ProtectTokens {
				continue
			}
			if msg.IsError || msg.ToolCallID == "" || strings.TrimSpace(msg.Content) == "" {
				continue
			}
			tokens := estimateTokens(msg)
			if tokens < config.FenceMinTokens {
				continue
			}
			label := msg.ToolName
			if label == "" {
				label = "tool"
			}
			regions = append(regions, ShakeRegion{
				Kind: "toolResult", MsgIndex: i, Tokens: tokens,
				Label: label, Original: msg.Content,
			})
			continue
		}
		if msg.Role != provider.RoleUser && msg.Role != provider.RoleAssistant {
			continue
		}
		if accumulatedAfter[i] < config.ProtectTokens {
			continue
		}
		for _, r := range scanTextForBlockRanges(msg.Content, config.FenceMinTokens) {
			r.MsgIndex = i
			r.Label = string(msg.Role)
			regions = append(regions, r)
		}
	}

	// Savings gate: a shake that reclaims less than minSavings is churn.
	savings := 0
	for _, r := range regions {
		savings += max(0, r.Tokens-PLACEHOLDER_TOKEN_ESTIMATE)
	}
	if savings < config.MinSavings {
		return nil
	}
	return regions
}

// ApplyShakeRegions replaces every region with its replacement text,
// highest-start-first so splicing one never shifts another's offsets
// (shake.ts:422). The input slice is not mutated; the result is new.
func ApplyShakeRegions(msgs []provider.Message, items []ShakeItem) []provider.Message {
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	// Group by message, descending offsets.
	byMsg := map[int][]ShakeItem{}
	for _, it := range items {
		byMsg[it.Region.MsgIndex] = append(byMsg[it.Region.MsgIndex], it)
	}
	for idx, list := range byMsg {
		text := out[idx].Content
		// Sort descending by start so splicing never shifts offsets.
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				if list[j].Region.Start > list[i].Region.Start {
					list[i], list[j] = list[j], list[i]
				}
			}
		}
		for _, it := range list {
			if it.Region.Kind == "toolResult" {
				text = it.Replacement
				continue
			}
			if it.Region.Start >= 0 && it.Region.End <= len(text) && it.Region.Start <= it.Region.End {
				text = text[:it.Region.Start] + it.Replacement + text[it.Region.End:]
			}
		}
		out[idx].Content = text
	}
	return out
}

// ShakeItem pairs a region with its replacement.
type ShakeItem struct {
	Region      ShakeRegion
	Replacement string
}

// ShakePlaceholder is the elided marker (shake.ts:7653).
func ShakePlaceholder(region ShakeRegion, offloadPath string) string {
	if offloadPath != "" {
		return fmt.Sprintf("[shaken ~%d tokens — recover: file://%s]", region.Tokens, offloadPath)
	}
	return fmt.Sprintf("[shaken ~%d tokens]", region.Tokens)
}

// max is defined by the Go runtime prelude in this package already; alias
// kept for the savings gate above.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
