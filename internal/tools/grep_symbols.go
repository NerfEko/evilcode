package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"evilcode/internal/lsp"
)

// grepRecord is one line emitted by ripgrep. Context lines use the same
// shape, but are marked so the renderer can distinguish them from matches.
type grepRecord struct {
	Path    string
	Line    int
	Text    string
	Context bool
	// Group identifies one ripgrep context block. A `--` separator means the
	// following context belongs to a different match group; keeping it here
	// prevents a limit trim from attaching that leading context to the last
	// retained hit.
	Group int
	// Binary is ripgrep's explicit-file match marker. It has no source line,
	// but dropping it would turn a successful search into an empty result.
	Binary bool
	Raw    string
}

// parseRGRecords accepts both ripgrep's match (path:line:text) and context
// (path-line-text) forms. Splitting on the first delimiter is not sufficient
// for paths containing '-' or ':', so the numeric line and matching delimiter
// are used as the unambiguous boundary.
func parseRGRecords(output string) []grepRecord {
	var records []grepRecord
	group := 0
	for rest := output; len(rest) > 0; {
		// A normal --null record is path NUL payload newline. A separator or
		// binary marker is a plain line and may appear before the next NUL.
		nul := strings.IndexByte(rest, 0)
		newline := strings.IndexByte(rest, '\n')
		if nul < 0 {
			for _, line := range strings.Split(strings.TrimRight(rest, "\n"), "\n") {
				if line == "--" {
					group++
					continue
				}
				if record, ok := parseRGPlainLine(line); ok {
					record.Group = group
					records = append(records, record)
				}
			}
			break
		}
		if newline >= 0 && newline < nul {
			// Do not split a newline-containing filename. Only consume a
			// pre-NUL line when it is a delimiter, binary marker, or a
			// deliberately supplied non-NUL record.
			line := rest[:newline]
			if line == "--" {
				group++
				rest = rest[newline+1:]
				continue
			}
			if record, ok := parseRGBinary("", line); ok {
				record.Group = group
				records = append(records, record)
				rest = rest[newline+1:]
				continue
			}
		}

		// --null makes the path boundary unambiguous even when a path
		// contains a colon, a dash followed by digits, or a newline. The
		// payload keeps ripgrep's match/context delimiter.
		path, payload := rest[:nul], rest[nul+1:]
		end := strings.IndexByte(payload, '\n')
		if end < 0 {
			end = len(payload)
		}
		line := payload[:end]
		if record, ok := parseRGPayload(line); ok {
			record.Path = path
			record.Group = group
			record.Raw = path + "\x00" + line
			records = append(records, record)
		} else if record, ok := parseRGBinary(path, line); ok {
			record.Group = group
			records = append(records, record)
		}
		if end == len(payload) {
			break
		}
		rest = payload[end+1:]
	}
	return records
}

func parseRGPlainLine(line string) (grepRecord, bool) {
	if line == "" || line == "--" {
		return grepRecord{}, false
	}
	if record, ok := parseRGBinary("", line); ok {
		return record, true
	}
	return parseRGRecord(line)
}

// parseRGPayload parses the part after --null's path terminator. In this
// shape the delimiter is at the start, so no path contents can be mistaken for
// a line number.
func parseRGPayload(payload string) (grepRecord, bool) {
	if payload == "" {
		return grepRecord{}, false
	}
	sep := byte(0)
	i := 0
	for i < len(payload) && payload[i] >= '0' && payload[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(payload) || (payload[i] != ':' && payload[i] != '-') {
		return grepRecord{}, false
	}
	sep = payload[i]
	lineNo := 0
	for _, digit := range payload[:i] {
		lineNo = lineNo*10 + int(digit-'0')
	}
	if lineNo < 1 {
		return grepRecord{}, false
	}
	return grepRecord{Line: lineNo, Text: payload[i+1:], Context: sep == '-'}, true
}

func parseRGRecord(line string) (grepRecord, bool) {
	for i := 0; i < len(line); i++ {
		sep := line[i]
		if sep != ':' && sep != '-' {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		if j == i+1 || j >= len(line) || line[j] != sep {
			continue
		}
		lineNo := 0
		for _, digit := range line[i+1 : j] {
			lineNo = lineNo*10 + int(digit-'0')
		}
		if lineNo < 1 {
			return grepRecord{}, false
		}
		return grepRecord{
			Path:    line[:i],
			Line:    lineNo,
			Text:    line[j+1:],
			Context: sep == '-',
		}, true
	}
	return grepRecord{}, false
}

func parseRGBinary(path, line string) (grepRecord, bool) {
	const marker = ": binary file matches"
	idx := strings.LastIndex(line, marker)
	if idx < 0 {
		return grepRecord{}, false
	}
	if path == "" {
		path = line[:idx]
	}
	if path == "" {
		return grepRecord{}, false
	}
	return grepRecord{Path: path, Text: strings.TrimSpace(line[idx+1:]), Binary: true, Raw: line}, true
}

// grepSymbol is the smallest useful description of a declaration. End is
// inclusive because grep reports human-facing one-based lines.
type grepSymbol struct {
	Name  string
	Kind  string
	Start int
	End   int
}

// grepOutline asks the language server for a complete file outline and uses
// the declaration scanner when no server is configured or it cannot answer in
// the short budget. The result deliberately matches the lsp tool's compact
// `line: kind name` shape, while keeping paths relative to the grep root.
func (e *Exec) grepOutline(ctx context.Context, path, display string) Result {
	symbols := e.outlineSymbols(ctx, path)
	if len(symbols) == 0 {
		return Result{Output: "No symbols in " + display + ".", Intent: "empty outline"}
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].Start != symbols[j].Start {
			return symbols[i].Start < symbols[j].Start
		}
		return symbols[i].Name < symbols[j].Name
	})
	var b strings.Builder
	for _, symbol := range symbols {
		fmt.Fprintf(&b, "%d: %s\n", symbol.Start, symbol.label())
	}
	return Result{
		Output: strings.TrimRight(b.String(), "\n"),
		Intent: fmt.Sprintf("%d symbols", len(symbols)),
	}
}

func (e *Exec) outlineSymbols(ctx context.Context, path string) []grepSymbol {
	if e.lspServer != nil {
		lspCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		client, err := e.lspServer.For(lspCtx, path)
		if err == nil && client != nil {
			if symbols, symbolErr := client.Symbols(lspCtx, path); symbolErr == nil {
				if out := flattenGrepSymbols(symbols); len(out) > 0 {
					cancel()
					return out
				}
			}
		}
		cancel()
	}
	return scanGrepSymbols(ctx, path, 0)
}

func (s grepSymbol) label() string {
	if s.Kind == "" {
		return s.Name
	}
	return s.Kind + " " + s.Name
}

// grepSymbols prefers the language server, but a failed or unavailable server
// must never turn a cheap search into a failed search. A short per-file budget
// keeps a server that is indexing from holding up every hit in the result.
func (e *Exec) grepSymbols(ctx context.Context, path string, unavailable map[string]bool, maxLine int) []grepSymbol {
	if e.lspServer != nil && !unavailable[lsp.LanguageID(path)] {
		lspCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		client, err := e.lspServer.For(lspCtx, path)
		if err != nil || client == nil {
			unavailable[lsp.LanguageID(path)] = true
		} else {
			if symbols, err := client.Symbols(lspCtx, path); err == nil {
				out := flattenGrepSymbols(symbols)
				if len(out) > 0 {
					cancel()
					return out
				}
			} else {
				unavailable[lsp.LanguageID(path)] = true
			}
		}
		cancel()
	}
	return scanGrepSymbols(ctx, path, maxLine)
}

func flattenGrepSymbols(symbols []lsp.Symbol) []grepSymbol {
	var out []grepSymbol
	var walk func([]lsp.Symbol)
	walk = func(list []lsp.Symbol) {
		for _, symbol := range list {
			start, end := lspSymbolLines(symbol)
			if start > 0 {
				out = append(out, grepSymbol{
					Name:  symbol.Name,
					Kind:  symbol.KindName(),
					Start: start,
					End:   end,
				})
			}
			walk(symbol.Children)
		}
	}
	walk(symbols)
	return out
}

func lspSymbolLines(symbol lsp.Symbol) (start, end int) {
	range_ := symbol.Range
	if zeroLSPRange(range_) && !zeroLSPRange(symbol.Location.Range) {
		range_ = symbol.Location.Range
	}
	if !zeroLSPRange(range_) {
		start = range_.Start.Line + 1
		end = range_.End.Line + 1
		// LSP range ends are exclusive. An end at column zero therefore
		// excludes the whole end line when the declaration spans multiple
		// lines; do not label the following declaration's first line as part
		// of this symbol.
		if range_.End.Line > range_.Start.Line && range_.End.Character == 0 {
			end--
		}
	}
	if start == 0 {
		start = symbol.Line()
	}
	if end < start {
		end = start
	}
	return start, end
}

func zeroLSPRange(r lsp.Range) bool {
	return r.Start.Line == 0 && r.Start.Character == 0 &&
		r.End.Line == 0 && r.End.Character == 0
}

// declaration patterns are deliberately small. They cover the languages
// commonly used in this repository without pretending that a regular
// expression is a parser; the language server path remains authoritative.
var (
	goFunctionDecl = regexp.MustCompile(`^\s*func\b\s*(?:\(([^)]*)\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	goDecl         = regexp.MustCompile(`^\s*(?:type|var|const)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rustDecl       = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(fn|struct|enum|trait|impl|mod|type|const|static)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	pythonDecl     = regexp.MustCompile(`^\s*(?:async\s+)?(def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	jsDecl         = regexp.MustCompile(`^\s*(?:(?:export|default|async)\s+)*(function|class)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	jsValueDecl    = regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][A-Za-z0-9_$]*)\s*=>`)
)

func scanGrepSymbols(ctx context.Context, path string, maxLine int) []grepSymbol {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var symbols []grepSymbol
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil
		}
		lineNo++
		if symbol, ok := declarationSymbol(scanner.Text(), lineNo); ok {
			symbols = append(symbols, symbol)
		}
		if maxLine > 0 && lineNo >= maxLine {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	for i := range symbols {
		if i+1 < len(symbols) {
			symbols[i].End = symbols[i+1].Start - 1
		} else {
			symbols[i].End = int(^uint(0) >> 1)
		}
	}
	return symbols
}

func declarationSymbol(line string, lineNo int) (grepSymbol, bool) {
	if match := goFunctionDecl.FindStringSubmatch(line); len(match) > 0 {
		name := match[2]
		if receiver := receiverName(match[1]); receiver != "" {
			name = receiver + "." + name
		}
		return grepSymbol{Name: name, Kind: "func", Start: lineNo}, true
	}
	if match := goDecl.FindStringSubmatch(line); len(match) > 0 {
		return grepSymbol{Name: match[1], Kind: strings.TrimSpace(strings.Fields(line)[0]), Start: lineNo}, true
	}
	if match := rustDecl.FindStringSubmatch(line); len(match) > 0 {
		return grepSymbol{Name: match[2], Kind: match[1], Start: lineNo}, true
	}
	if match := pythonDecl.FindStringSubmatch(line); len(match) > 0 {
		return grepSymbol{Name: match[2], Kind: match[1], Start: lineNo}, true
	}
	if match := jsDecl.FindStringSubmatch(line); len(match) > 0 {
		return grepSymbol{Name: match[2], Kind: match[1], Start: lineNo}, true
	}
	if match := jsValueDecl.FindStringSubmatch(line); len(match) > 0 {
		return grepSymbol{Name: match[1], Kind: "function", Start: lineNo}, true
	}
	return grepSymbol{}, false
}

func receiverName(receiver string) string {
	// A pointer marks the base type unambiguously, including generic receivers
	// such as `s *Widget[T]`; taking the final identifier there would return the
	// type parameter (`T`) rather than the receiver (`Widget`).
	if star := strings.IndexByte(receiver, '*'); star >= 0 {
		fields := strings.FieldsFunc(receiver[star+1:], func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
		})
		if len(fields) > 0 {
			return fields[0]
		}
	}
	fields := strings.FieldsFunc(receiver, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	if len(fields) == 0 {
		return ""
	}
	// Named receivers conventionally start lower-case (`w Widget[T]`), while
	// an unnamed receiver is just its type (`Widget[T]`). Keep the first token
	// for the latter and skip the variable name for the former.
	if len(fields) > 1 {
		first, _ := utf8.DecodeRuneInString(fields[0])
		if first == '_' || unicode.IsLower(first) {
			return fields[1]
		}
	}
	return fields[0]
}

func enclosingGrepSymbol(symbols []grepSymbol, line int) string {
	if len(symbols) == 0 {
		return ""
	}
	best := -1
	for i, symbol := range symbols {
		if line < symbol.Start || line > symbol.End {
			continue
		}
		if best < 0 || symbol.End-symbol.Start < symbols[best].End-symbols[best].Start {
			best = i
		}
	}
	if best < 0 {
		// Do not infer an enclosing symbol past an explicit LSP range. Range
		// ends are exclusive, so the line immediately after a declaration is
		// top level unless another range actually contains it.
		return ""
	}
	return symbols[best].label()
}
