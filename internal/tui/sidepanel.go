package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

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

	// Body is free-form pinned content (a `/btw` answer, pinned markdown).
	Body []string
}

// Empty reports whether there is nothing to show.
func (p PanelContent) Empty() bool {
	return p.Diff == "" && len(p.Body) == 0
}

// RenderSidePanel draws the pane: a single left border column and a one-row
// header (plan.md §3.3). The border carries focus, so the pane never needs a
// full box.
func (r *Renderer) RenderSidePanel(c PanelContent, mode DiffMode, width, height int, focused bool) []string {
	if width < MinDiffWidth || height <= 0 {
		return nil
	}

	borderColor := theme.RGB(70, 70, 70)
	if focused {
		borderColor = theme.RGB(130, 130, 160)
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(borderColor)))
	dim := r.style(theme.RoleDim)

	inner := width - 2
	title := c.Title
	if title == "" {
		title = "Panel"
	}

	var body []string
	switch {
	case c.Empty():
		body = []string{dim.Render("nothing pinned yet")}
	case c.Diff != "" && mode == DiffFile:
		body = r.fileDiffLines(c.Path, c.Diff, inner)
	case c.Diff != "":
		body = r.renderDiffLang(c.Diff, langFromPath(c.Path))
	default:
		body = c.Body
	}

	out := make([]string, 0, height)
	out = append(out, border.Render("│ ")+dim.Render(truncateCells(title, inner)))
	for _, line := range body {
		if len(out) >= height {
			break
		}
		out = append(out, border.Render("│ ")+truncateCells(line, inner))
	}
	for len(out) < height {
		out = append(out, border.Render("│"))
	}
	return out
}

// fileDiffLines renders the whole-file view of §9.4.
//
// A deleted line gets a blank number rather than the number it used to have:
// it does not exist in the new file, and printing a number invites the reader
// to go look at a line that says something else.
func (r *Renderer) fileDiffLines(path, diff string, width int) []string {
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffAdd)))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffDel)))
	gutter := r.style(theme.RoleDim)
	body := r.style(theme.RoleAIText)
	lang := langFromPath(path)

	type row struct {
		num    int // 0 means no number
		marker string
		text   string
	}

	var rows []row
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
			rows = append(rows, row{lineNo, "+", line[1:]})
		case strings.HasPrefix(line, "-"):
			rows = append(rows, row{0, "-", line[1:]})
		case line == "":
			continue
		default:
			lineNo++
			text := line
			if strings.HasPrefix(text, " ") {
				text = text[1:]
			}
			rows = append(rows, row{lineNo, " ", text})
		}
	}
	if len(rows) == 0 {
		return []string{gutter.Render("no changes")}
	}

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
