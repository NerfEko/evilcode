// Shake persists the region contents to a recoverable offload doc, replaces
// each region with a placeholder, and atomically rewrites the log. The omp
// orchestration (agent-session.shake, #saveShakeArtifact, #runAutoShake)
// mapped onto evilcode's storage model.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
)

// ShakeResult reports what a shake reclaimed.
type ShakeResult struct {
	ToolResultsDropped int
	BlocksDropped      int
	TokensFreed        int
	OffloadPath        string
}

// Shake elides every eligible region in the named session log. mode
// "aggressive" drops everything; the default protects the recent tail.
// The original contents land in one offload document under
// <dataDir>/shakes/ so the agent can read them back.
func Shake(dataDir, name string, aggressive bool, settings compact.Settings) (ShakeResult, error) {
	path, err := pathFor(dataDir, name)
	if err != nil {
		return ShakeResult{}, err
	}
	entries, err := Read(path)
	if err != nil {
		return ShakeResult{}, err
	}

	// Decode messages in order with their entry positions.
	var msgs []provider.Message
	positions := []int{}
	for i, e := range entries {
		switch e.Type {
		case TypeUser, TypeAssistant, TypeTool:
		default:
			continue
		}
		var m provider.Message
		if err := json.Unmarshal(e.Data, &m); err != nil {
			continue
		}
		msgs = append(msgs, m)
		positions = append(positions, i)
	}

	cfg := compact.AggressiveShakeConfig()
	if !aggressive {
		cfg = compact.DefaultShakeConfig()
	}
	// Skip the summarized prefix: everything before the newest compaction
	// marker + its recent-context message is already summarized away.
	skipBefore := 0
	for i, m := range msgs {
		if compact.IsCompactionMarker(m) || compact.IsCompactionRecentMarker(m) {
			skipBefore = i + 1
		}
	}
	cfg.SkipBefore = skipBefore

	regions := compact.CollectShakeRegions(msgs, cfg)
	if len(regions) == 0 {
		return ShakeResult{}, nil
	}

	// Offload doc: one file, region-indexed, so placeholders can point at it.
	offloadPath, err := saveShakeArtifact(dataDir, regions)
	if err != nil || offloadPath == "" {
		offloadPath = "" // degrade to bare placeholders
	}

	items := make([]compact.ShakeItem, len(regions))
	var originalTokens, replacementTokens int
	result := ShakeResult{OffloadPath: offloadPath}
	for i, region := range regions {
		if region.Kind == "toolResult" {
			result.ToolResultsDropped++
		} else {
			result.BlocksDropped++
		}
		originalTokens += region.Tokens
		replacement := compact.ShakePlaceholder(region, offloadPath)
		replacementTokens += countTokensShake(replacement)
		items[i] = compact.ShakeItem{Region: region, Replacement: replacement}
	}

	shaken := compact.ApplyShakeRegions(msgs, items)

	if err := backup(path); err != nil {
		return ShakeResult{}, err
	}

	var b strings.Builder
	write := func(e Entry) {
		if line, err := json.Marshal(e); err == nil {
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	msgIdx := 0
	shakenIdx := map[int]bool{}
	for i := range regions {
		shakenIdx[regions[i].MsgIndex] = true
	}
	for _, e := range entries {
		switch e.Type {
		case TypeUser, TypeAssistant, TypeTool:
		default:
			write(e)
			continue
		}
		if msgIdx < len(msgs) && shakenIdx[msgIdx] {
			data, err := encodeMessage(path, shaken[msgIdx])
			if err != nil {
				return ShakeResult{}, err
			}
			e.Data = data
		}
		msgIdx++
		write(e)
	}
	if err := writeSessionFile(path, []byte(b.String())); err != nil {
		return ShakeResult{}, err
	}
	result.TokensFreed = originalTokens - replacementTokens
	return result, nil
}

// saveShakeArtifact concatenates the original region contents into one
// recoverable document (agent-session #saveShakeArtifact). evilcode keeps it
// in <dataDir>/shakes/; a write failure degrades to bare placeholders.
func saveShakeArtifact(dataDir string, regions []compact.ShakeRegion) (string, error) {
	dir := filepath.Join(dataDir, "shakes")
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, r := range regions {
		b.WriteString(fmt.Sprintf("### region %d (%s, ~%d tokens)\n\n%s\n\n", i+1, r.Label, r.Tokens, r.Original))
	}
	path := filepath.Join(dir, fmt.Sprintf("shake-%s.md", time.Now().Format("20060102-150405")))
	if err := os.WriteFile(path, []byte(b.String()), FilePerm); err != nil {
		return "", err
	}
	return path, nil
}

func countTokensShake(s string) int { return len(s) / 4 }
