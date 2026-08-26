package tui

import (
	"fmt"
	"reflect"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/core"
	"evilcode/internal/theme"
)

// DiffMode is how diffs are presented (plan.md §9.3, cycled by Alt+G).
type DiffMode int

const (
	// DiffOff hides diffs entirely; tool rows still show their +/- counts.
	DiffOff DiffMode = iota

	// DiffInline renders diffs in the transcript.
	DiffInline

	// DiffPinned keeps the most recent diff in the side panel.
	DiffPinned

	// DiffFile shows the whole file with change gutters (§9.4).
	DiffFile

	numDiffModes
)

func (d DiffMode) String() string {
	switch d {
	case DiffOff:
		return "off"
	case DiffPinned:
		return "pinned"
	case DiffFile:
		return "file"
	default:
		return "inline"
	}
}

// Next cycles the mode.
func (d DiffMode) Next() DiffMode { return (d + 1) % numDiffModes }

// UsesPanel reports whether the mode needs the side pane open.
func (d DiffMode) UsesPanel() bool { return d == DiffPinned || d == DiffFile }

// PanelContent is what the side pane is currently showing.
type PanelContent struct {
	Title string

	// Path and Diff describe a file change.
	Path string
	Diff string

	// Body is free-form pinned content (a `/btw` answer, pinned markdown), or
	// the file's current lines when Diff is set: the pane then renders the
	// whole file with the diff's changes marked instead of the truncated diff.
	Body []string

	// Code asks the panel to syntax-highlight Body as a file preview. It is
	// used by read quick views; ordinary side-panel text stays plain.
	Code bool

	// Numbers asks the panel to draw a line-number gutter, the same one the
	// diff views use, so a read and an edit of the same file line up.
	Numbers bool

	// ScrollTo is the body line to center when the content is first shown, so
	// a clicked edit opens at the change rather than at the top of the file.
	ScrollTo int
}

// Empty reports whether there is nothing to show.
func (p PanelContent) Empty() bool {
	return p.Diff == "" && len(p.Body) == 0
}

// panelBodyCacheKey identifies a rendered pane body. The Body slice is keyed
// by its backing array's identity rather than its contents: pane bodies are
// replaced wholesale when a file changes, so pointer identity is exact and
// cheap where a content hash would re-read every line. The palette pointer
// matters because the body is styled with it.
type panelBodyCacheKey struct {
	title, path, diff string
	code, numbers     bool
	mode              DiffMode
	width             int
	bodyLen           int
	bodyPtr           uintptr
	palettePtr        uintptr
}

func panelBodyKeyFor(c PanelContent, mode DiffMode, width int, palette *theme.Palette) panelBodyCacheKey {
	key := panelBodyCacheKey{
		title: c.Title, path: c.Path, diff: c.Diff, code: c.Code, numbers: c.Numbers,
		mode: mode, width: width, bodyLen: len(c.Body),
	}
	if len(c.Body) > 0 {
		key.bodyPtr = reflect.ValueOf(c.Body).Pointer()
	}
	if palette != nil {
		key.palettePtr = reflect.ValueOf(palette).Pointer()
	}
	return key
}

// RenderSidePanel draws the pane: a single left border column, a one-row
// header, the windowed body, and a footer row (plan.md §3.3). The border
// carries focus, so the pane never needs a full box. scroll is the body line
// the window starts at, and live paints the green "live" tag in the footer.
func (r *Renderer) RenderSidePanel(c PanelContent, mode DiffMode, width, height int, focused bool, scroll int, live bool) []string {
	if width < MinDiffWidth || height <= 0 {
		return nil
	}
	body := r.panelBody(c, mode, width-2)
	viewport := max(height-2, 0)
	scroll = clamp(scroll, 0, Max(len(body), viewport))
	end := min(scroll+viewport, len(body))
	return r.renderPanelChrome(c.Title, body[scroll:end], width, height, focused, live)
}

// panelBody renders the pane's full body: the whole file with its diff, the
// diff alone, or the pinned text. It is split from the chrome so the caller
// can measure the body height and window it without rendering twice.
func (r *Renderer) panelBody(c PanelContent, mode DiffMode, width int) []string {
	dim := r.style(theme.RoleDim)
	switch {
	case c.Empty():
		return []string{dim.Render("nothing pinned yet")}
	case c.Diff != "" && len(c.Body) > 0:
		// A clicked edit carries the file's current lines: show the entire
		// file with the change marked, not the truncated diff.
		return r.wholeFileDiff(c.Path, c.Body, c.Diff, width)
	case c.Diff != "" && mode == DiffFile:
		return r.fileDiffLines(c.Path, c.Diff, width)
	case c.Diff != "":
		return r.renderDiffLang(c.Diff, langFromPath(c.Path))
	case c.Code && isMarkdown(c.Path):
		// A whole file, unlike a diff, can go through glamour: there is no
		// gutter to keep aligned and no per-line correspondence to preserve, so
		// the reader gets the document rather than its source (see the diff
		// path above, which stays line-exact on purpose).
		return strings.Split(
			r.AtWidth(width).Markdown.Render(strings.Join(c.Body, "\n"), true), "\n")
	case c.Code && c.Numbers:
		// A read gets the same line-number gutter as the diff views, so a
		// read and an edit of the same file line up.
		return r.numberedLines(c.Path, c.Body, width)
	case c.Code:
		return HighlightLines(langFromPath(c.Path), strings.Join(c.Body, "\n"))
	default:
		body := make([]string, len(c.Body))
		for i, line := range c.Body {
			body[i] = core.SanitizeTerminal(line)
		}
		return body
	}
}

// renderPanelChrome wraps a windowed body with the border, the header, and the
// footer row: the green "live" tag bottom-left when live view is on, and the
// key hints bottom-right.
func (r *Renderer) renderPanelChrome(title string, body []string, width, height int, focused, live bool) []string {
	borderColor := theme.RGB(70, 70, 70)
	if focused {
		borderColor = theme.RGB(130, 130, 160)
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(borderColor)))
	dim := r.style(theme.RoleDim)

	inner := width - 2
	// The panel shows a file path, a diff, or an answer from a side call —
	// none of it ours. The diff branches route through the highlighter, which
	// sanitizes; Body and Title do not, so they are cleaned here.
	title = core.SanitizeTerminal(title)
	if title == "" {
		title = "Panel"
	}

	out := make([]string, 0, height)
	out = append(out, border.Render("│ ")+dim.Render(truncateCells(title, inner)))
	for _, line := range body {
		if len(out) >= height-1 {
			break
		}
		out = append(out, border.Render("│ ")+truncateCells(line, inner))
	}
	for len(out) < height-1 {
		out = append(out, border.Render("│"))
	}

	left := ""
	if live {
		left = lipgloss.NewStyle().
			Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleSuccess))).Render("live")
	}
	right := dim.Render("ctrl+q to close, ctrl+L for live view")
	avail := inner - lipgloss.Width(left)
	if lipgloss.Width(right) > avail {
		right = truncateCells(right, max(avail, 0))
	}
	out = append(out, border.Render("│ ")+left+
		strings.Repeat(" ", max(avail-lipgloss.Width(right), 0))+right)
	return out
}

// diffRow is one line of a gutter view: a number (0 means none), a change
// marker, and the text.
type diffRow struct {
	num    int
	marker string
	text   string
}

// renderDiffRows draws gutter rows: number, marker, then the line, highlighted
// and tinted so code keeps its shape while still reading as an add or delete.
func (r *Renderer) renderDiffRows(rows []diffRow, lang string, width int) []string {
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffAdd)))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffDel)))
	gutter := r.style(theme.RoleDim)
	body := r.style(theme.RoleAIText)

	// Gutter width is at least three, so a short file does not look cramped
	// next to a long one.
	digits := 3
	for _, rw := range rows {
		if n := len(fmt.Sprint(rw.num)); n > digits {
			digits = n
		}
	}

	out := make([]string, 0, len(rows))
	for _, rw := range rows {
		num := strings.Repeat(" ", digits)
		if rw.num > 0 {
			num = fmt.Sprintf("%*d", digits, rw.num)
		}

		var style lipgloss.Style
		switch rw.marker {
		case "+":
			style = addStyle
		case "-":
			style = delStyle
		default:
			style = body
		}

		text := truncateCells(rw.text, max(width-digits-3, 8))
		// Highlight then tint, so code keeps its shape (§9.3).
		var rendered string
		if lines := tokenize(lang, text); len(lines) > 0 && rw.marker == " " {
			rendered = renderTokens(lines[0], nil)
		} else {
			rendered = style.Render(text)
		}

		out = append(out, gutter.Render(num+" │")+style.Render(rw.marker)+rendered)
	}
	return out
}

// numberedLines renders a file preview with the same line-number gutter the
// diff views use, so a read and an edit of the same file line up.
func (r *Renderer) numberedLines(path string, body []string, width int) []string {
	rows := make([]diffRow, len(body))
	for i, line := range body {
		rows[i] = diffRow{num: i + 1, marker: " ", text: line}
	}
	return r.renderDiffRows(rows, langFromPath(path), width)
}

// fileDiffLines renders the whole-file view of §9.4.
//
// A deleted line gets a blank number rather than the number it used to have:
// it does not exist in the new file, and printing a number invites the reader
// to go look at a line that says something else.
func (r *Renderer) fileDiffLines(path, diff string, width int) []string {
	var rows []diffRow
	lineNo := 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue
		case strings.HasPrefix(line, "@@"):
			// Hunk headers carry the new-file start line.
			if n, ok := parseHunkStart(line); ok {
				lineNo = n - 1
			}
			continue
		case strings.HasPrefix(line, "+"):
			lineNo++
			rows = append(rows, diffRow{lineNo, "+", line[1:]})
		case strings.HasPrefix(line, "-"):
			rows = append(rows, diffRow{0, "-", line[1:]})
		case line == "":
			continue
		default:
			lineNo++
			text := line
			if strings.HasPrefix(text, " ") {
				text = text[1:]
			}
			rows = append(rows, diffRow{lineNo, " ", text})
		}
	}
	if len(rows) == 0 {
		return []string{r.style(theme.RoleDim).Render("no changes")}
	}
	return r.renderDiffRows(rows, langFromPath(path), width)
}

// wholeFileDiff renders the entire file with the diff's changes marked in the
// gutter: added lines carry their number and a +, deleted lines are inserted
// where they were removed with a blank number and a -. The reader gets the
// file around the change instead of a truncated diff.
func (r *Renderer) wholeFileDiff(path string, fileLines []string, diff string, width int) []string {
	lang := langFromPath(path)

	// Walk the hunks once: added lines are marked by their new-file number,
	// and deleted lines are queued at the file index where they were removed.
	added := map[int]bool{}
	deletedAt := map[int][]string{}
	lineNo, inHunk := 0, false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			if n, ok := parseHunkStart(line); ok {
				lineNo, inHunk = n, true
			}
		case inHunk && strings.HasPrefix(line, "+"):
			added[lineNo] = true
			lineNo++
		case inHunk && strings.HasPrefix(line, "-"):
			at := min(lineNo-1, len(fileLines))
			deletedAt[at] = append(deletedAt[at], line[1:])
		case inHunk && strings.HasPrefix(line, " "):
			lineNo++
		}
	}

	var rows []diffRow
	flush := func(i int) {
		for _, d := range deletedAt[i] {
			rows = append(rows, diffRow{0, "-", d})
		}
	}
	for i, line := range fileLines {
		flush(i)
		marker := " "
		if added[i+1] {
			marker = "+"
		}
		rows = append(rows, diffRow{i + 1, marker, line})
	}
	flush(len(fileLines))
	if len(rows) == 0 {
		return []string{r.style(theme.RoleDim).Render("no changes")}
	}
	return r.renderDiffRows(rows, lang, width)
}

// diffScrollLine returns the 0-based file index of the first hunk's first
// new-file line, so a whole-file view can open at the change.
func diffScrollLine(diff string) int {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			if n, ok := parseHunkStart(line); ok {
				return max(n-1, 0)
			}
		}
	}
	return 0
}

// parseHunkStart pulls the new-file start line out of `@@ -a,b +c,d @@`.
func parseHunkStart(header string) (int, bool) {
	plus := strings.Index(header, "+")
	if plus < 0 {
		return 0, false
	}
	rest := header[plus+1:]
	end := strings.IndexAny(rest, ", @")
	if end < 0 {
		return 0, false
	}
	n := 0
	for _, r := range rest[:end] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n > 0
}
