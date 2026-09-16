// Cut point detection — the port of omp's compaction.ts cut-point section
// (findValidCutPoints, findTurnStartIndex, findCutPoint, prepareCompaction).
//
// omp cuts between session entries; evilcode compacts []provider.Message.
// The invariant set is identical: never cut inside a tool-result run, prefer
// turn boundaries, and a mid-turn cut splits the turn into a summarized
// prefix plus a kept suffix.
package compact

import (
	"strings"

	"evilcode/internal/provider"
)

// Settings are the compaction knobs, omp CompactionSettings semantics
// (compaction.ts:144). Zero value is not usable; use DefaultSettings.
type Settings struct {
	Enabled           bool
	Strategy          string // "context-full" | "handoff" | "shake" | "off"
	ThresholdPercent  int    // 1..99, or <=0 meaning window minus reserve
	ThresholdTokens   int    // >0 wins over the percentage
	ReserveTokens     int
	KeepRecentTokens  int
	AutoContinue      bool
	PruneReads        bool
	HandoffSaveToDisk bool
}

// DefaultSettings — omp DEFAULT_COMPACTION_SETTINGS (compaction.ts:156).
func DefaultSettings() Settings {
	return Settings{
		Enabled:          true,
		Strategy:         "context-full",
		ThresholdPercent: -1,
		ThresholdTokens:  -1,
		ReserveTokens:    16384,
		KeepRecentTokens: 20000,
		AutoContinue:     true,
		PruneReads:       true,
	}
}

// EffectiveReserveTokens is omp effectiveReserveTokens (compaction.ts:218):
// at least 15% of the window or the configured floor, whichever is larger.
func EffectiveReserveTokens(contextWindow int, s Settings) int {
	reserve := s.ReserveTokens
	if reserve < 16384 {
		reserve = 16384
	}
	r := contextWindow * 15 / 100
	if r > reserve {
		return r
	}
	return reserve
}

// ResolveThresholdTokens — omp resolveThresholdTokens (compaction.ts:250).
// A fixed token limit wins over a percentage; percentage <= 0 means the
// window minus the effective reserve.
func ResolveThresholdTokens(contextWindow int, s Settings) int {
	if s.ThresholdTokens > 0 {
		t := s.ThresholdTokens
		if t > contextWindow-1 {
			t = contextWindow - 1
		}
		if t < 1 {
			t = 1
		}
		return t
	}
	if s.ThresholdPercent > 0 {
		p := s.ThresholdPercent
		if p > 99 {
			p = 99
		}
		if p < 1 {
			p = 1
		}
		return contextWindow * p / 100
	}
	return contextWindow - EffectiveReserveTokens(contextWindow, s)
}

// ShouldCompact — omp shouldCompact (compaction.ts:225): compaction fires
// when context tokens exceed the threshold. There is deliberately no
// projection or topic-shift trigger: omp compacts when the context is
// genuinely near full, plus dedicated overflow/incomplete recovery paths.
func ShouldCompact(contextTokens, contextWindow int, s Settings) bool {
	if !s.Enabled || s.Strategy == "off" || contextWindow <= 0 {
		return false
	}
	return contextTokens > ResolveThresholdTokens(contextWindow, s)
}

// CompactionContextTokens feeds the trigger decision: the larger of the
// provider-reported usage and the local estimate of the stored conversation
// (compaction.ts:246). The provider report is normally ground truth, but a
// deflated report must never let the real history grow unbounded.
func CompactionContextTokens(providerContextTokens, storedConversationEstimate int) int {
	if providerContextTokens < 0 {
		providerContextTokens = 0
	}
	if storedConversationEstimate < 0 {
		storedConversationEstimate = 0
	}
	if storedConversationEstimate > providerContextTokens {
		return storedConversationEstimate
	}
	return providerContextTokens
}

// CutPointResult names where the conversation splits (compaction.ts:457).
type CutPointResult struct {
	// FirstKept is the message index where the kept recent history starts.
	FirstKept int
	// TurnStart is the index of the user message starting the split turn,
	// or -1 when the cut does not split a turn.
	TurnStart int
	// IsSplitTurn reports that the cut landed mid-turn.
	IsSplitTurn bool
}

// findValidCutPoints returns the indices where a cut is legal: user or
// assistant messages. Never a tool result — a result must follow its call
// (compaction.ts:398).
func findValidCutPoints(msgs []provider.Message, start, end int) []int {
	var cut []int
	for i := start; i < end; i++ {
		switch msgs[i].Role {
		case provider.RoleUser, provider.RoleAssistant:
			cut = append(cut, i)
		}
	}
	return cut
}

// findTurnStartIndex walks back to the user message opening the turn that
// contains idx (compaction.ts:440).
func findTurnStartIndex(msgs []provider.Message, idx, start int) int {
	for i := idx; i >= start; i-- {
		if msgs[i].Role == provider.RoleUser {
			return i
		}
	}
	return -1
}

// FindCutPoint walks backwards from the newest message accumulating token
// estimates and cuts at the closest valid cut point at or after the message
// that crosses keepRecentTokens (compaction.ts:482).
//
// Unlike evilcode's previous turn-based selection, omp may cut at an
// assistant message: its tool results follow it and stay in the kept tail.
func FindCutPoint(msgs []provider.Message, start, end, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(msgs, start, end)
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKept: start, TurnStart: -1}
	}

	cutIndex := cutPoints[0]
	accumulated := 0
	for i := end - 1; i >= start; i-- {
		accumulated += estimateTokens(msgs[i])
		if accumulated >= keepRecentTokens {
			for _, c := range cutPoints {
				if c >= i {
					cutIndex = c
					break
				}
			}
			break
		}
	}

	cutEntry := msgs[cutIndex]
	isUser := cutEntry.Role == provider.RoleUser
	turnStart := -1
	if !isUser {
		turnStart = findTurnStartIndex(msgs, cutIndex, start)
	}
	return CutPointResult{
		FirstKept:   cutIndex,
		TurnStart:   turnStart,
		IsSplitTurn: !isUser && turnStart != -1,
	}
}

// Preparation is everything the summarizer needs — omp CompactionPreparation
// (compaction.ts:884) mapped to evilcode messages.
type Preparation struct {
	// MessagesToSummarize is the old prefix, summarized and then discarded.
	MessagesToSummarize []provider.Message
	// TurnPrefixMessages is the summarized front half of a split turn.
	TurnPrefixMessages []provider.Message
	// RecentMessages is the verbatim tail kept after compaction.
	RecentMessages []provider.Message
	// IsSplitTurn reports a mid-turn cut.
	IsSplitTurn bool
	// TokensBefore is the context size that triggered compaction.
	TokensBefore int
	// PreviousSummary is the prior compaction summary, for the update path.
	PreviousSummary string
	// PreviousRecentContext is the prior serialized recent context. It
	// becomes ordinary summarized input after a compaction.
	PreviousRecentContext string
}

// keepRecentCalibrated applies omp's ratio recalibration (compaction.ts:926):
// when the provider reports more prompt tokens than the local estimate
// counts, the keep-recent budget shrinks by the same ratio so the tail stays
// honest against real billing.
func keepRecentCalibrated(keepRecentTokens, providerPromptTokens, estimatedTokens int) int {
	if estimatedTokens <= 0 || providerPromptTokens <= 0 {
		return keepRecentTokens
	}
	ratio := float64(providerPromptTokens) / float64(estimatedTokens)
	if ratio <= 1 {
		return keepRecentTokens
	}
	adjusted := int(float64(keepRecentTokens) / ratio)
	if adjusted < 1 {
		adjusted = 1
	}
	return adjusted
}

// PrepareCompaction splits msgs at the omp cut point. It returns nil when
// compaction would be a no-op: nothing to summarize (compaction.ts:906).
//
// boundaryStart skips a prior compaction's synthetic messages — they are
// already summary state, never re-summarized raw.
func PrepareCompaction(msgs []provider.Message, s Settings, tokensBefore, providerPromptTokens int) *Preparation {
	if len(msgs) == 0 {
		return nil
	}
	if isCompactionMarker(msgs[len(msgs)-1]) {
		// The newest entry is already a summary; a second compaction in a
		// row has nothing new to fold.
		return nil
	}

	boundaryStart := 0
	for i, msg := range msgs {
		if isCompactionMarker(msg) || isCompactionRecentMarker(msg) {
			boundaryStart = i + 1
		}
	}

	estimated := estimateMessagesTokens(msgs[boundaryStart:])
	keepRecent := keepRecentCalibrated(s.KeepRecentTokens, providerPromptTokens, estimated)

	cut := FindCutPoint(msgs, boundaryStart, len(msgs), keepRecent)

	historyEnd := cut.FirstKept
	if cut.IsSplitTurn {
		historyEnd = cut.TurnStart
	}

	var toSummarize, turnPrefix, recent []provider.Message
	toSummarize = append(toSummarize, msgs[boundaryStart:historyEnd]...)
	if cut.IsSplitTurn {
		turnPrefix = append(turnPrefix, msgs[cut.TurnStart:cut.FirstKept]...)
	}
	recent = append(recent, msgs[cut.FirstKept:]...)

	if len(toSummarize) == 0 && len(turnPrefix) == 0 {
		return nil
	}

	// The kept tail must be tool-consistent: every tool call inside it has
	// its result with it, and no result references a summarized call. omp
	// guarantees this by construction (it never cuts between a call and its
	// result); the check is evilcode's fail-closed guard carried over.
	if safeToolBoundary(msgs, cut.FirstKept) == 0 && cut.FirstKept > 0 {
		return nil
	}

	var prevSummary, prevRecent string
	for _, msg := range msgs[:boundaryStart] {
		if isCompactionMarker(msg) {
			prevSummary = trimSummaryPrefix(msg.Content)
		}
		if isCompactionRecentMarker(msg) {
			prevRecent = trimRecentPrefix(msg.Content)
		}
	}

	return &Preparation{
		MessagesToSummarize:   toSummarize,
		TurnPrefixMessages:    turnPrefix,
		RecentMessages:        recent,
		IsSplitTurn:           cut.IsSplitTurn,
		TokensBefore:          tokensBefore,
		PreviousSummary:       prevSummary,
		PreviousRecentContext: prevRecent,
	}
}

func trimSummaryPrefix(content string) string {
	const p = "[conversation compacted]\n\n"
	if len(content) > len(p) && content[:len(p)] == p {
		return strings.TrimSpace(content[len(p):])
	}
	return strings.TrimSpace(content)
}

func trimRecentPrefix(content string) string {
	const p = "[conversation recent context]\n\n"
	if len(content) > len(p) && content[:len(p)] == p {
		return strings.TrimSpace(content[len(p):])
	}
	return strings.TrimSpace(content)
}
