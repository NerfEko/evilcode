// Prune rewrites a session log in place, blanking read results superseded by
// a later read of the same file. omp prunes its per-entry store directly
// (pruning.ts:248); evilcode's equivalent is a whole-log rewrite through the
// same .bak + atomic-replace machinery compaction uses.
package session

import (
	"encoding/json"
	"strings"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
)

// PruneSupersededReads rewrites the named session log, replacing every read
// result superseded by a newer read of the same path with the
// SUPERSEDED_NOTICE placeholder. Returns the number of results pruned and
// the approximate tokens reclaimed.
//
// The rewrite is atomic (backup → temp → rename) and leaves the conversation
// shape untouched: only tool-result content changes, so provider pairing and
// resume replay stay valid. A log whose messages cannot be decoded round-trips
// unchanged — pruning must never damage a session it does not understand.
func PruneSupersededReads(dataDir, name string) (pruned int, tokensSaved int, err error) {
	path, err := pathFor(dataDir, name)
	if err != nil {
		return 0, 0, err
	}
	entries, err := Read(path)
	if err != nil {
		return 0, 0, err
	}

	// Decode message entries in order; each maps to its entry position.
	var msgs []provider.Message
	positions := make([]int, 0, len(entries))
	for i, e := range entries {
		switch e.Type {
		case TypeUser, TypeAssistant, TypeTool:
		default:
			continue
		}
		var m provider.Message
		if err := json.Unmarshal(e.Data, &m); err != nil {
			// An undecodable entry is left exactly as-is; pruning never
			// trades a known shape for a guessed one.
			continue
		}
		msgs = append(msgs, m)
		positions = append(positions, i)
	}

	candidates := compact.FindSupersededReads(msgs, nil)
	if len(candidates) == 0 {
		return 0, 0, nil
	}
	pruned = len(candidates)
	for _, c := range candidates {
		tokensSaved += c.Tokens
	}

	prunedMsgs := compact.PruneMessages(msgs, candidates)

	if err := backup(path); err != nil {
		return 0, 0, err
	}

	var b strings.Builder
	write := func(e Entry) {
		if line, err := json.Marshal(e); err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	// positions[k] is the entry index of msgs[k]; only pruned positions get
	// re-encoded, everything else round-trips verbatim.
	prunedMsg := map[int]bool{}
	for _, c := range candidates {
		prunedMsg[c.Index] = true
	}
	for i, e := range entries {
		k := -1
		for n, p := range positions {
			if p == i {
				k = n
				break
			}
		}
		if k < 0 || !prunedMsg[k] {
			write(e)
			continue
		}
		data, err := encodeMessage(path, prunedMsgs[k])
		if err != nil {
			return 0, 0, err
		}
		e.Data = data
		write(e)
	}
	if err := writeSessionFile(path, []byte(b.String())); err != nil {
		return 0, 0, err
	}
	return pruned, tokensSaved, nil
}
