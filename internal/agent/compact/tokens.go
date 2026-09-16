// Package compact ports oh-my-pi's compaction engine
// (packages/agent/src/compaction) onto evilcode's provider/session model.
//
// The design lives in docs/plan-compaction-port.md. Storage stays evilcode's
// atomic rewrite (.bak + summary + serialized tail); everything omp layers on
// top of storage — token accounting, serialization, prompts, cut points, file
// operations, model candidates, pruning, shake, handoff — is ported here.
package compact

import (
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"

	"evilcode/internal/provider"
)

// IMAGE_TOKEN_ESTIMATE charges a fixed amount per image, matching what
// providers typically bill for inline images. Images have no tokenizer
// representation (compaction.ts:275).
const IMAGE_TOKEN_ESTIMATE = 1200

var (
	tiktokenOnce sync.Once
	tiktokenBPE  *tiktoken.Tiktoken
	tiktokenErr  error
)

// bpe returns the cl100k_base encoding, built once. The vocabulary comes
// from the tiktoken-go-loader's embedded assets, so nothing here touches the
// network at runtime.
func bpe() (*tiktoken.Tiktoken, error) {
	tiktokenOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
		tiktokenBPE, tiktokenErr = tiktoken.GetEncoding("cl100k_base")
	})
	return tiktokenBPE, tiktokenErr
}

// countTokens counts cl100k_base tokens for s. This is not the first-party
// tokenizer of any specific provider, but it lands within ~5-10% for English
// and code, which is what the compaction budgets need (compaction.ts:277).
// An empty string costs nothing; a BPE failure falls back to the bytes/4
// rule rather than failing compaction over a tokenizer.
func countTokens(s string) int {
	if s == "" {
		return 0
	}
	enc, err := bpe()
	if err != nil || enc == nil {
		return (len(s) + 3) / 4
	}
	return len(enc.Encode(s, nil, nil))
}

// countTokensMax bounds countTokens at a ceiling, used where omp caps a
// summarizer output budget.
func countTokensMax(s string, max int) int {
	if max <= 0 {
		return 0
	}
	n := countTokens(s)
	if n > max {
		return max
	}
	return n
}

// estimateTokens estimates one message the way omp's compaction estimator
// does: text, reasoning, tool-call envelopes, provider items and repairs all
// count; images cost the fixed estimate (compaction.ts:289).
func estimateTokens(msg provider.Message) int {
	n := countTokens(msg.Content)
	if msg.Reasoning != "" {
		n += countTokens(msg.Reasoning)
	}
	for _, call := range msg.ToolCalls {
		n += countTokens(call.ID) + countTokens(call.Name) + countTokens(string(call.Args))
	}
	for _, item := range msg.ProviderItems {
		n += countTokens(string(item))
	}
	for _, repair := range msg.Repairs {
		n += countTokens(repair)
	}
	n += len(msg.Images) * IMAGE_TOKEN_ESTIMATE
	if n <= 0 {
		return 1
	}
	return n
}

// estimateMessagesTokens sums estimateTokens over a message slice.
func estimateMessagesTokens(msgs []provider.Message) int {
	total := 0
	for _, msg := range msgs {
		total += estimateTokens(msg)
	}
	return total
}
