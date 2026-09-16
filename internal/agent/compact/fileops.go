// File operation tracking — the port of omp's compaction/utils.ts
// file-operations section. Read/write operations are extracted from the
// summarized messages' tool calls and appended to the summary as one
// <files> block. This is the model-independent continuity anchor: exact
// paths survive any summarizer.
package compact

import (
	"encoding/json"
	"sort"
	"strings"

	"evilcode/internal/provider"
)

// FILE_OPERATION_SUMMARY_LIMIT caps how many paths the <files> block lists
// per group (utils.ts:153).
const FILE_OPERATION_SUMMARY_LIMIT = 20

// FileOperations tracks what the summarized stretch did to files
// (utils.ts:16). Read records read-paths (deduped, selector-stripped);
// Written records paths with read-then-write (RW) merged in.
type FileOperations struct {
	// Read maps a stripped path to any recorded selector label.
	Read map[string][]string
	// rw marks paths that were also written, so the files block can mark
	// them RW.
	rw map[string]bool
	// Written preserves write order.
	Written []string
}

func newFileOps() *FileOperations {
	return &FileOperations{Read: map[string][]string{}, rw: map[string]bool{}}
}

// RANGE_LIST_SRC mirrors evilcode's read-selector grammar: a trailing
// `:chunk` is a selector only when it is a line-range list, `raw`, or
// `conflicts`. omp mirrors its own tool grammar the same way (utils.ts:35).
const (
	readRangeOnlySrc = `^(?:L?\d+(?:(?:[-+]|\.\.)L?\d+|-|\.\.)?(?:,(?:L?\d+(?:(?:[-+]|\.\.)L?\d+|-|\.\.)?))*|raw|conflicts)$`
)

// SplitReadSelector splits a read-tool path into its base path and trailing
// selector, mirroring omp splitReadSelector (utils.ts:42). Returns path only.
func SplitReadSelector(p string) string {
	// evilcode selectors: path:50, path:50-200, path:5-16,960-973, path:raw,
	// path:conflicts — matched from the last colon.
	i := strings.LastIndex(p, ":")
	if i <= 0 {
		return p
	}
	sel := p[i+1:]
	// Cheap prefilter before regex: selectors are ASCII and short.
	if selLooksLikeRange(sel) {
		return p[:i]
	}
	return p
}

func selLooksLikeRange(sel string) bool {
	if sel == "" {
		return false
	}
	if sel == "raw" || sel == "conflicts" {
		return true
	}
	// L?\d+([-+]\d+|-\d+|\.\.\d+|…)?, comma-list form.
	n := len(sel)
	i := 0
	expectNum := true
	for i < n {
		c := sel[i]
		if expectNum {
			if c == 'L' || c == 'l' {
				i++
			}
			start := i
			for i < n && sel[i] >= '0' && sel[i] <= '9' {
				i++
			}
			if i == start {
				return false
			}
			expectNum = false
			if i >= n {
				return true
			}
			c = sel[i]
		}
		switch c {
		case ',':
			expectNum = true
			i++
		case '-':
			i++
			expectNum = true
		case '+':
			i++
			expectNum = true
		case '.':
			if i+1 < n && sel[i+1] == '.' {
				i += 2
				expectNum = true
			} else {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ExtractFileOpsFromMessage pulls read/write/edit paths out of one
// assistant message's tool calls. evilcode's tool set: read, edit, write,
// bash, grep, glob, lsp… Only filesystem-mutating and read tools carry
// paths worth pinning (utils.ts:97).
func (f *FileOperations) ExtractFromMessage(msg provider.Message) {
	switch msg.Role {
	case provider.RoleAssistant:
		for _, call := range msg.ToolCalls {
			f.addCall(call.Name, call.Args)
		}
	}
}

func (f *FileOperations) addCall(name string, rawArgs []byte) {
	var args map[string]any
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return
	}
	path, _ := args["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" || isURLSchemePath(path) {
		return
	}
	switch name {
	case "read":
		p := SplitReadSelector(path)
		f.Read[p] = appendUnique(f.Read[p], p)
	case "edit", "write":
		f.markRW(SplitReadSelector(path))
	case "bash":
		// omp extracts nothing from bash; cd is tracked separately there and
		// the working directory lives in session meta. Nothing to do.
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func (f *FileOperations) markRW(path string) {
	if !f.rw[path] {
		f.rw[path] = true
		f.Written = append(f.Written, path)
		delete(f.Read, path)
	}
}

// isURLSchemePath reports a path that is actually a URL — internal URIs and
// web URLs never belong in the filesystem <files> block (utils.ts:86).
func isURLSchemePath(p string) bool {
	i := strings.Index(p, "://")
	return i > 0 && schemeChar(p[i-1])
}

func schemeChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ComputeFileLists returns the read-only and modified lists (utils.ts:136).
func (f *FileOperations) ComputeFileLists() (readFiles, modifiedFiles []string) {
	for p := range f.Read {
		readFiles = append(readFiles, p)
	}
	sort.Strings(readFiles)
	modifiedFiles = append(modifiedFiles, f.Written...)
	sort.Strings(modifiedFiles)
	return readFiles, modifiedFiles
}

// FormatFileOperations renders one grouped <files> tag, prefix-folded and
// capped per group (utils.ts:164). readSet marks paths that were read at
// some point; a modified path also in readSet is shown RW.
func FormatFileOperations(readFiles, modifiedFiles []string, readSet map[string]bool) string {
	if len(readFiles) == 0 && len(modifiedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<files>\n")
	if len(modifiedFiles) > 0 {
		b.WriteString(group("Modified", modifiedFiles, readSet, "RW"))
	}
	if len(readFiles) > 0 {
		b.WriteString(group("Read", readFiles, readSet, ""))
	}
	b.WriteString("</files>")
	return b.String()
}

func group(label string, paths []string, readSet map[string]bool, readMark string) string {
	var b strings.Builder
	// Prefix folding: group consecutive entries sharing a directory.
	var dirs []string
	byDir := map[string][]string{}
	for _, p := range paths {
		mark := ""
		if readMark != "" {
			mark = readMark
		} else if readSet != nil && readSet[p] {
			mark = "RW"
		}
		dir, base := splitLastSlash(p)
		if byDir[dir] == nil {
			dirs = append(dirs, dir)
		}
		if mark != "" {
			base += " (" + mark + ")"
		}
		if len(byDir[dir]) < FILE_OPERATION_SUMMARY_LIMIT {
			byDir[dir] = append(byDir[dir], base)
		} else if len(byDir[dir]) == FILE_OPERATION_SUMMARY_LIMIT {
			byDir[dir] = append(byDir[dir], "…")
		}
	}
	sort.Strings(dirs)
	for i, dir := range dirs {
		if i == 0 {
			b.WriteString(label + ":\n")
		}
		dirLabel := dir
		if dirLabel == "" {
			dirLabel = "."
		}
		b.WriteString(dirLabel + "/: " + strings.Join(byDir[dir], ", ") + "\n")
	}
	return b.String()
}

func splitLastSlash(p string) (dir, base string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// UpsertFileOperations strips any existing <files> block from summary and
// appends the fresh one, so repeated compactions never accumulate stale
// lists (utils.ts:181).
func UpsertFileOperations(summary string, readFiles, modifiedFiles []string, readSet map[string]bool) string {
	summary = stripFileOperationTags(summary)
	block := FormatFileOperations(readFiles, modifiedFiles, readSet)
	if block == "" {
		return strings.TrimSpace(summary)
	}
	if strings.TrimSpace(summary) == "" {
		return block
	}
	return strings.TrimSpace(summary) + "\n\n" + block
}

func stripFileOperationTags(summary string) string {
	var out []string
	for _, section := range strings.Split(summary, "\n\n") {
		s := strings.TrimSpace(section)
		if strings.HasPrefix(s, "<files>\n") && strings.HasSuffix(s, "</files>") {
			continue
		}
		out = append(out, section)
	}
	return strings.Join(out, "\n\n")
}
