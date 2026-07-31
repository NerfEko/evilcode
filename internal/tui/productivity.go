package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/ansirender"
	"evilcode/internal/session"
	"evilcode/internal/theme"
)

// Stats is what `/productivity` reports.
type Stats struct {
	Sessions int
	Messages int
	Prompts  int

	// TokensIn and TokensOut are what the session log recorded, which is the
	// provider's own count rather than an estimate.
	TokensIn  int
	TokensOut int

	// ByDay is prompts per day, oldest last, for the sparkline.
	ByDay []DayCount

	First time.Time
	Last  time.Time

	// Busiest is the day with the most prompts.
	Busiest DayCount
}

// DayCount is one day's activity.
type DayCount struct {
	Day     time.Time
	Prompts int
}

// CollectStats reads the session store.
//
// It reads the logs rather than keeping a counter, so the numbers survive a
// crash and cover sessions from before the feature existed — a stats dashboard
// that starts at zero the day you build it is not worth having.
func CollectStats(dataDir string) (Stats, error) {
	var s Stats
	infos, err := session.List(dataDir)
	if err != nil {
		return s, err
	}

	byDay := map[string]*DayCount{}
	for _, info := range infos {
		s.Sessions++
		s.Messages += info.Messages
		if s.First.IsZero() || info.Modified.Before(s.First) {
			s.First = info.Modified
		}
		if info.Modified.After(s.Last) {
			s.Last = info.Modified
		}

		// Modified rather than a start time: Info records when a session was
		// last written, which is the only timestamp the store keeps.
		day := info.Modified.Truncate(24 * time.Hour)
		key := day.Format("2006-01-02")
		if byDay[key] == nil {
			byDay[key] = &DayCount{Day: day}
		}
		byDay[key].Prompts += info.Messages
		s.Prompts += info.Messages
	}

	for _, d := range byDay {
		s.ByDay = append(s.ByDay, *d)
		if d.Prompts > s.Busiest.Prompts {
			s.Busiest = *d
		}
	}
	sort.Slice(s.ByDay, func(i, j int) bool { return s.ByDay[i].Day.Before(s.ByDay[j].Day) })
	return s, nil
}

// ProductivityDays is how much history the dashboard shows.
const ProductivityDays = 30

// RenderProductivity draws the dashboard.
//
// It is styled text rather than a chart library: the frame is already an ANSI
// grid, and `evilcode probe render` turns any frame into a PNG — so the "stats
// dashboard → PNG" of the plan needs no second rendering path.
func (r *Renderer) RenderProductivity(s Stats, width int) []string {
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 150, 200)))).Bold(true)
	label := r.style(theme.RoleDim)
	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(200, 200, 210)))).Bold(true)

	rows := []string{title.Render("📊 evilcode · what you have been doing")}
	if s.Sessions == 0 {
		return append(rows, label.Render("Nothing recorded yet."))
	}

	stat := func(name, v string) string {
		return label.Render(fmt.Sprintf("%-14s", name)) + value.Render(v)
	}
	rows = append(rows,
		"",
		stat("sessions", fmt.Sprint(s.Sessions)),
		stat("messages", fmt.Sprint(s.Messages)),
	)
	if !s.First.IsZero() {
		rows = append(rows, stat("since", s.First.Format("2 Jan 2006")))
	}
	if s.Busiest.Prompts > 0 {
		rows = append(rows, stat("busiest day",
			fmt.Sprintf("%s · %d messages",
				s.Busiest.Day.Format("2 Jan"), s.Busiest.Prompts)))
	}

	if spark := Sparkline(s.ByDay, ProductivityDays); spark != "" {
		rows = append(rows, "",
			label.Render(fmt.Sprintf("last %d days", ProductivityDays)),
			lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 200, 255)))).Render(spark))
	}
	return rows
}

// SparkChars is the ramp, low to high.
var SparkChars = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders daily counts as a single row.
//
// Days with no activity are drawn as a blank rather than as the lowest bar, so
// a gap reads as a gap. A ramp that shows "nothing happened" and "a little
// happened" identically is a chart that lies.
func Sparkline(days []DayCount, window int) string {
	if len(days) == 0 {
		return ""
	}
	if len(days) > window {
		days = days[len(days)-window:]
	}

	peak := 0
	for _, d := range days {
		peak = max(peak, d.Prompts)
	}
	if peak == 0 {
		return ""
	}

	var b strings.Builder
	for _, d := range days {
		if d.Prompts == 0 {
			b.WriteRune(' ')
			continue
		}
		// Scaled so any activity at all reaches the first bar.
		idx := (d.Prompts-1)*(len(SparkChars)-1)/peak + 1
		b.WriteRune(SparkChars[min(idx, len(SparkChars)-1)])
	}
	return b.String()
}

// productivityCommand implements `/productivity`.
func (m *Model) productivityCommand() tea.Cmd {
	if m.dataDir == "" {
		m.notice = "no session store to report on"
		return nil
	}
	stats, err := CollectStats(m.dataDir)
	if err != nil {
		m.notice = "could not read the session store: " + err.Error()
		return nil
	}

	rows := m.renderer.RenderProductivity(stats, m.chatWidth())
	m.blocks = append(m.blocks, Block{
		Kind: BlockNotice,
		Text: strings.Join(plainRows(rows), "\n"),
	})
	m.scroll.FollowBottom()

	// The PNG is the deliverable the plan asks for: a dashboard an agent can
	// look at without a human present (§0.2, §14).
	if path, err := m.writeProductivityPNG(rows); err == nil {
		m.notice = "📊 " + path
	}
	return nil
}

// writeProductivityPNG renders the dashboard through the same ANSI→PNG path the
// probe rig uses (§14), so there is one renderer rather than two — and so the
// dashboard is something an agent can look at with no human present.
func (m *Model) writeProductivityPNG(rows []string) (string, error) {
	dir := filepath.Join(m.dataDir, "shots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "productivity.png")

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := ansirender.WritePNG(f, strings.Join(rows, "\n")+"\n"); err != nil {
		return "", err
	}
	return path, nil
}
