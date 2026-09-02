package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/core"
	"evilcode/internal/provider"
	"evilcode/internal/theme"
)

// HeaderState describes the session for the top-of-transcript header.
type HeaderState struct {
	SessionName string
	Version     string

	Provider string
	Model    string
	AuthKind string

	// ReasoningEffort is shown when the active provider supports live effort
	// control. It stays beside the model so the setting is visible without
	// spending another header row.
	ReasoningEffort provider.ReasoningEffort
	// ReasoningEfforts is the active model's ordered capability list. It is
	// optional for ordinary local construction and is useful to attached TUI
	// clients that learn capabilities from the daemon snapshot.
	ReasoningEfforts []provider.ReasoningEffort

	// Cwd and Branch describe the workspace.
	Cwd    string
	Branch string

	// Providers lists configured providers and whether each is authenticated,
	// for the status dots.
	Providers []ProviderStatus

	// Skills and MCP servers, when present.
	Skills []string
	MCP    []MCPStatus

	// Attached is the daemon socket this TUI is driving through, empty for a
	// local session. ClientName names this client (plan.md §20): with two
	// terminals on one session, "which of these am I" is the first question,
	// and the header is where it gets answered.
	Attached   string
	ClientName string
}

// MCPStatus is one connected server, for the header line.
type MCPStatus struct {
	Name      string
	Tools     int
	Connected bool
	LastError string
}

// ProviderStatus is one provider dot.
type ProviderStatus struct {
	Name  string
	Ready bool
}

// RenderHeader draws the borderless, all-dim header of plan.md §8.7.
func (r *Renderer) RenderHeader(h HeaderState) []string {
	dim := r.style(theme.RoleDim)
	name := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleHeaderName))).Bold(true)
	system := r.style(theme.RoleSystem)
	sessionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleHeaderSession)))

	var out []string

	first := name.Render("evilcode")
	if h.Version != "" {
		first += dim.Render(" · " + h.Version)
	}
	out = append(out, first)

	if h.SessionName != "" {
		emoji := core.CreatureEmoji(h.SessionName)
		title := sessionStyle.Render(core.Title(h.SessionName, emoji))
		if h.Attached != "" {
			// Attached mode names both ends. The session belongs to the daemon,
			// so calling it "session:" as though it were local would be a lie
			// the moment a second terminal attaches to the same one.
			line := dim.Render("server: ") + title
			if h.ClientName != "" {
				line += dim.Render(" · client: ") +
					sessionStyle.Render(core.Title(h.ClientName, core.CreatureEmoji(h.ClientName)))
			}
			out = append(out, line)
		} else {
			out = append(out, dim.Render("session: ")+title)
		}
	}

	modelLine := dim.Render("/model to switch")
	if h.AuthKind != "" && h.Provider != "" {
		modelLine += dim.Render(" · " + h.AuthKind + ":" + h.Provider)
	}
	if h.Model != "" {
		modelLine += dim.Render(" · ") + system.Render(h.Model)
	}
	if h.ReasoningEffort.Valid() {
		modelLine += dim.Render(" · effort:") + system.Render(string(h.ReasoningEffort))
	}
	out = append(out, modelLine)

	// Provider dots: filled and colored when authenticated, hollow and dim
	// when not, so an unconfigured provider is visible without being loud.
	if len(h.Providers) > 0 {
		var dots []string
		for _, p := range h.Providers {
			if p.Ready {
				dots = append(dots, r.style(theme.RoleSuccess).Render("●")+dim.Render(" "+p.Name))
			} else {
				dots = append(dots, dim.Render("○ "+p.Name))
			}
		}
		out = append(out, strings.Join(dots, dim.Render("  ")))
	}

	if len(h.MCP) > 0 {
		var parts []string
		shown := h.MCP
		suffix := ""
		if len(shown) > 3 {
			suffix = fmt.Sprintf(" +%d more", len(shown)-3)
			shown = shown[:3]
		}
		for _, s := range shown {
			if s.Connected {
				parts = append(parts, fmt.Sprintf("%s (%d tools)", s.Name, s.Tools))
				continue
			}
			// A dead server must be visible: "down" plus a bounded slice of the
			// last error, so a wedged or absent server is diagnosable from the
			// header instead of just failing silently.
			err := s.LastError
			if len(err) > 64 {
				err = err[:64] + "…"
			}
			if err != "" {
				parts = append(parts, fmt.Sprintf("%s (down: %s)", s.Name, err))
			} else {
				parts = append(parts, fmt.Sprintf("%s (down)", s.Name))
			}
		}
		out = append(out, dim.Render("mcp: "+strings.Join(parts, ", ")+suffix))
	}

	if len(h.Skills) > 0 {
		shown := h.Skills
		suffix := ""
		if len(shown) > 4 {
			suffix = fmt.Sprintf(" +%d more", len(shown)-4)
			shown = shown[:4]
		}
		out = append(out, dim.Render("skills: /"+strings.Join(shown, " /")+suffix))
	}

	loc := h.Cwd
	if h.Branch != "" {
		loc += fmt.Sprintf(" (%s)", h.Branch)
	}
	if loc != "" {
		out = append(out, dim.Render(loc))
	}
	return out
}

// WelcomeMessage is kept for compatibility with callers that used the old
// start-page copy. The start page now renders the EvilCode wordmark instead.
const WelcomeMessage = "Welcome to evilcode 🦇"

// evilCodeTitleArt is the compact small-font figlet treatment. Keeping the
// generated shape as its own rows makes the title easy to recognize and lets
// the shimmer paint individual glyph cells without rebuilding any preview.
var evilCodeTitleArt = []string{
	"         _ _            _",
	" _____ _(_) |__ ___  __| |___",
	"/ -_) V / | / _/ _ \\/ _\x60 / -_)",
	"\\___|\\_/|_|_\\__\\___/\\__,_\\___|",
}

const startPageWordmarkRows = 4

const startPageWaveCycle = 40

func evilCodeTitleWidth() int {
	width := 0
	for _, line := range evilCodeTitleArt {
		width = max(width, lipgloss.Width(line))
	}
	return width
}

func evilCodeTitleLines(width int) []string {
	blockWidth := evilCodeTitleWidth()
	left := max((width-blockWidth)/2, 0)
	lines := make([]string, len(evilCodeTitleArt))
	for i, line := range evilCodeTitleArt {
		if width > 0 && blockWidth > width {
			line = truncateCells(line, width)
		}
		lines[i] = strings.Repeat(" ", left) + line
	}
	return lines
}

func (r *Renderer) startPageWordmark(width, frame int) []string {
	lines := evilCodeTitleLines(width)
	mauve := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleUser))).Bold(true)
	white := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleUserText))).Bold(true)
	wordmarkWidth := evilCodeTitleWidth()
	left := max((width-wordmarkWidth)/2, 0)
	base := left - 1 + frame%startPageWaveCycle
	rowOffsets := [...]int{-1, 0, 1, 2}
	for row, line := range lines {
		var styled strings.Builder
		for col, ch := range []rune(line) {
			if ch == ' ' {
				styled.WriteRune(ch)
				continue
			}
			center := base + rowOffsets[row]
			distance := col - center
			if distance < 0 {
				distance = -distance
			}
			if distance <= 1 {
				styled.WriteString(white.Render(string(ch)))
			} else {
				styled.WriteString(mauve.Render(string(ch)))
			}
		}
		lines[row] = styled.String()
	}
	return lines
}

// RenderStartPage draws the empty-transcript start page: an EvilCode wordmark,
// a live preview of the selected session's conversation, and a horizontal row
// of resume buttons you scroll through with ←/→.
//
// The eye/black-hole idle art and the rotating starter-prompt chips that used to
// live here are gone — the start page is for either typing a new prompt or
// jumping back into a session you already have running.
//
// width/height are the transcript slot's size, so the preview box fills the
// available space and the buttons sit just above the composer. Widgets are
// suppressed while this is showing: an empty screen decorated with status boxes
// is busier than the thing it decorates (plan.md §8.3).
func (r *Renderer) RenderStartPage(rows []SessionRow, selected int, active bool, width, height int) []string {
	return r.renderStartPage(rows, selected, active, width, height, 0)
}

func (r *Renderer) renderStartPage(rows []SessionRow, selected int, active bool, width, height, waveFrame int) []string {
	hint := r.style(theme.RoleDim)
	title := r.startPageWordmark(width, waveFrame)

	if len(rows) == 0 {
		// Packed, not bottom-pinned: the wordmark hugs the composer and the
		// rows below stay blank. That blank space is where the slash palette
		// floats when it opens — the overlay splices in below the composer
		// instead of being forced above it and covering the status line, which
		// is what bottom-pinning did (it broke the "palette never moves the
		// transcript" invariant, plan.md §5.2). With sessions, the preview box
		// fills the slot for real, so this only shapes the empty greeting.
		out := append([]string(nil), title...)
		out = append(out, "", hint.Render("  no other sessions yet — type below to start a new one"))
		return out
	}

	sel := clamp(selected, 0, len(rows)-1)

	// Fixed chrome: wordmark/wave + gap + blank + buttons + hint.
	chrome := len(title) + 4
	boxH := max(height-chrome, 6)
	if boxH > height {
		boxH = height
	}

	out := append([]string(nil), title...)
	out = append(out, "")
	out = append(out, r.startPreviewBox(rows, sel, width, boxH)...)
	out = append(out, "")
	out = append(out, r.startPageButtonRow(rows, sel, active, width))
	out = append(out, hint.Render("  Type to start a new session  ·  ←/→ pick a session  ·  Enter to resume"))

	// Pad to the requested height so the layout fills and the composer hugs the
	// bottom rather than floating.
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// startPreviewBox draws a full-width bordered box showing the selected
// session's recent conversation (rendered the way the transcript renders it),
// with the session's name and full activity status as the title. It is the live
// preview of "what is happening" in that session.
func (r *Renderer) startPreviewBox(rows []SessionRow, sel, width, height int) []string {
	border := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(130, 130, 160))))
	dim := r.style(theme.RoleDim)

	row := rows[sel]
	status := r.sessionStatus(row)
	meta := fmt.Sprintf("%d messages", row.Info.Messages)
	if row.Info.Model != "" {
		meta += " · " + row.Info.Model
	}
	if age := humanAge(row.Info.Modified); age != "" {
		meta += " · " + age
	}
	title := " " + row.Info.Emoji + " " + row.Info.Name + "  " + plainText(status) + "  " + meta + " "
	titleW := lipgloss.Width(plainText(title))
	top := "╭" + title + strings.Repeat("─", max(width-titleW-2, 0)) + "╮"
	out := []string{border.Render(top)}

	closeBox := func() []string {
		for len(out) < height-1 {
			out = append(out, border.Render("│"))
		}
		return append(out, border.Render("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	}

	inner := max(width-4, 20)
	sub := r.AtWidth(inner)
	var convo []string
	for i := range row.Preview {
		convo = append(convo, sub.render(&row.Preview[i])...)
	}
	// Tail the conversation: the most recent context is what tells you whether
	// this is the session you meant.
	body := height - 2 // top + bottom borders
	if len(convo) > body {
		convo = convo[len(convo)-body:]
	}
	for _, line := range convo {
		out = append(out, border.Render("│")+" "+truncateCells(line, inner))
	}
	_ = dim
	return closeBox()
}

// startPageButtonRow draws the horizontal row of resume buttons. Each button is
// a compact pill (emoji + name + status glyph); the selected one is filled. When
// the pills do not all fit, the row side-scrolls to keep the selected pill
// visible, with ‹/› markers when edges are cut off.
func (r *Renderer) startPageButtonRow(rows []SessionRow, sel int, active bool, width int) string {
	type pill struct {
		render string
		w      int
	}
	pills := make([]pill, 0, len(rows))
	for i, row := range rows {
		pills = append(pills, pill{render: r.startPagePill(row, i == sel && active), w: lipgloss.Width(plainText(r.startPagePill(row, false)))})
	}

	sep := "  "
	sepW := lipgloss.Width(sep)
	total := 0
	for i, p := range pills {
		if i > 0 {
			total += sepW
		}
		total += p.w
	}

	// Everything fits: lay the pills out left-aligned.
	if total <= width {
		var b strings.Builder
		for i, p := range pills {
			if i > 0 {
				b.WriteString(sep)
			}
			b.WriteString(p.render)
		}
		line := b.String()
		if pad := width - lipgloss.Width(plainText(line)); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		return line
	}

	// Side-scroll: grow a window around the selected pill until it would
	// overflow, preferring to center the selection.
	lo, hi := sel, sel
	used := pills[sel].w
	canLeft := func() bool { return lo > 0 && used+sepW+pills[lo-1].w <= width }
	canRight := func() bool { return hi < len(pills)-1 && used+sepW+pills[hi+1].w <= width }
	for {
		// Alternate expanding left then right so the selection stays near the
		// middle of the window.
		if canLeft() {
			lo--
			used += sepW + pills[lo].w
		}
		if canRight() {
			hi++
			used += sepW + pills[hi].w
		}
		if !canLeft() && !canRight() {
			break
		}
	}

	var b strings.Builder
	if lo > 0 {
		b.WriteString(r.style(theme.RoleDim).Render("‹ "))
	}
	for i := lo; i <= hi; i++ {
		if i > lo {
			b.WriteString(sep)
		}
		b.WriteString(pills[i].render)
	}
	if hi < len(pills)-1 {
		b.WriteString(r.style(theme.RoleDim).Render(" ›"))
	}
	line := b.String()
	if pad := width - lipgloss.Width(plainText(line)); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return line
}

// startPagePill renders one compact horizontal button: emoji + name + a status
// glyph. The selected pill gets a filled background so it reads as a button.
func (r *Renderer) startPagePill(row SessionRow, selected bool) string {
	emoji := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(110, 210, 255)))).
		Render(row.Info.Emoji)

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	if selected {
		nameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(20, 20, 24)))).
			Background(lipgloss.Color(theme.Hex(theme.RGB(140, 220, 160)))).Bold(true)
	}

	return " " + emoji + " " + nameStyle.Render(row.Info.Name) + " " + r.startStatusGlyph(row) + " "
}

// startStatusGlyph is the one-character activity marker colored by state. The
// full status text lives in the preview box title; the pill only needs a dot.
func (r *Renderer) startStatusGlyph(row SessionRow) string {
	switch {
	case row.Pending > 0:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 190, 100)))).Render("◉")
	case row.Live && row.Running:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 190, 100)))).Render("◉")
	case row.Live:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(100, 220, 130)))).Render("●")
	case row.Info.Crashed:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(220, 100, 100)))).Render("💥")
	default:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(100, 100, 100)))).Render("✓")
	}
}
