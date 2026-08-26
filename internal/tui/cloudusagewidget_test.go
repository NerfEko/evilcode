package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/cloudusage"
	"evilcode/internal/config"
)

func TestCloudUsageWidget(t *testing.T) {
	r := testRenderer(80)
	now := time.Date(2026, 8, 4, 8, 46, 0, 0, time.UTC)
	snap := &cloudusage.Snapshot{
		Plan: "Free",
		Windows: []cloudusage.Window{
			{
				Label:       "Session",
				UsedPercent: 2.2,
				ResetsAt:    now.Add(14 * time.Minute),
				Segments: []cloudusage.Segment{
					{Model: "web search", Requests: 2, ColorHex: "#3b82f6", WidthPct: 5.7},
					{Model: "deepseek-v4-flash:0731", Requests: 68, ColorHex: "#4f46e5", WidthPct: 94.3},
				},
			},
			{
				Label:       "Weekly",
				UsedPercent: 0.8,
				ResetsAt:    now.Add(3 * time.Hour),
				Segments: []cloudusage.Segment{
					{Model: "glm-5.2", Requests: 17, ColorHex: "#3b82f6", WidthPct: 43.7},
					{Model: "deepseek-v4-flash:0731", Requests: 84, ColorHex: "#4f46e5", WidthPct: 53.7},
				},
			},
		},
	}

	rows := plainLines(r.CloudUsageWidget(snap, nil, now).Lines)
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"Session", "Weekly",
		"2%", "1%", // rounded 2.2 / 0.8
		"70 req", "101 req", // request totals
		"resets in 14m", "resets in 3h",
		"● web search", "● deepseek-v4-flash:0731", "● glm-5.2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("widget missing %q:\n%s", want, joined)
		}
	}
}

func TestCloudUsageWidgetAbsentUntilData(t *testing.T) {
	r := testRenderer(80)
	// Nothing fetched, no error: the widget is absent, not an empty box.
	if w := r.CloudUsageWidget(nil, nil, time.Now()); len(w.Lines) != 0 {
		t.Errorf("no data should produce no widget, got %v", w.Lines)
	}
}

func TestCloudUsageWidgetActionableErrors(t *testing.T) {
	r := testRenderer(80)
	now := time.Now()

	w := r.CloudUsageWidget(nil, cloudusage.ErrNotLoggedIn, now)
	joined := strings.Join(plainLines(w.Lines), "\n")
	if !strings.Contains(joined, "session expired") {
		t.Errorf("expired session should say so:\n%s", joined)
	}

	w = r.CloudUsageWidget(nil, cloudusage.ErrNoUsageData, now)
	joined = strings.Join(plainLines(w.Lines), "\n")
	if !strings.Contains(joined, "no usage data") {
		t.Errorf("no-usage error should say so:\n%s", joined)
	}

	// A transient network error does not nag; the widget stays absent.
	w = r.CloudUsageWidget(nil, errors.New("cloudusage: fetching: boom"), now)
	if len(w.Lines) != 0 {
		t.Errorf("transient error should keep the widget absent, got %v", w.Lines)
	}
}

func TestCloudUsageWidgetStaleDataWithError(t *testing.T) {
	r := testRenderer(80)
	now := time.Now()
	snap := &cloudusage.Snapshot{Windows: []cloudusage.Window{
		{Label: "Session", UsedPercent: 50},
	}}
	rows := plainLines(r.CloudUsageWidget(snap, cloudusage.ErrNotLoggedIn, now).Lines)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "50%") || !strings.Contains(joined, "session expired") {
		t.Errorf("stale snapshot should keep the bar and note the error:\n%s", joined)
	}
}

func TestCloudBarCellAssignment(t *testing.T) {
	// 68 of 70 requests belong to deepseek, so ~97% of a full bar's cells are
	// that model's color; the web-search slice is one cell at the very front.
	w := cloudusage.Window{
		UsedPercent: 100,
		Segments: []cloudusage.Segment{
			{Model: "web search", Requests: 2, ColorHex: "#3b82f6"},
			{Model: "deepseek-v4-flash:0731", Requests: 68, ColorHex: "#4f46e5"},
		},
	}
	bar := cloudBar(w, 100)
	if strings.Count(bar, "▱") != 0 {
		t.Errorf("full bar should have no track cells: %q", bar)
	}
	if width := lipgloss.Width(bar); width != cloudBarCells {
		t.Errorf("bar width = %d cells, want %d", width, cloudBarCells)
	}
	// The deepseek style is applied to most cells: render its segment style and
	// expect it inside the bar.
	deep := segmentStyle(w.Segments[1]).Render("▰")
	if !strings.Contains(bar, deep) {
		t.Errorf("bar should contain deepseek-colored cells: %q", bar)
	}
	// Zero-request segments still get a share (floor of 1), and no crash.
	zero := cloudusage.Window{UsedPercent: 100, Segments: []cloudusage.Segment{
		{Model: "a", Requests: 0}, {Model: "b", Requests: 0},
	}}
	if got := cloudBar(zero, 100); lipgloss.Width(got) != cloudBarCells {
		t.Errorf("zero-request bar = %d cells, want %d", lipgloss.Width(got), cloudBarCells)
	}
}

func TestModelFallbackColorStable(t *testing.T) {
	a := modelFallbackColor("deepseek-v4-flash:0731")
	b := modelFallbackColor("deepseek-v4-flash:0731")
	if a != b {
		t.Errorf("same model should map to the same fallback color: %v vs %v", a, b)
	}
	if modelFallbackColor("glm-5.2") == modelFallbackColor("deepseek-v4-flash:0731") {
		// Distinct models MAY collide on a small palette, but this pair should
		// not — it is the exact pair the fixture exercises.
		t.Error("fixture models should get distinct fallback colors")
	}
}

// The Cloud Usage widget only appears once a fetch has landed, and a fetch
// only ever starts when the user configured a session cookie — its presence is
// the opt-in. Golden runs are network-free: Deterministic mode never fetches.
func TestCloudUsageWidgetGatedByData(t *testing.T) {
	m := NewModel(nil, HeaderState{Model: "deepseek-v4-flash:0731", Provider: "ollama-cloud"})
	for _, w := range m.activeWidgets() {
		if w.Kind == WidgetCloudUsage {
			t.Error("widget must not appear before any fetch landed")
		}
	}

	snap := &cloudusage.Snapshot{Windows: []cloudusage.Window{
		{Label: "Session", UsedPercent: 2.2},
	}}
	m.cloudUsage = snap
	seen := false
	for _, w := range m.activeWidgets() {
		if w.Kind == WidgetCloudUsage {
			seen = true
		}
	}
	if !seen {
		t.Error("widget should appear once a snapshot exists")
	}
}

func TestMaybeRefreshCloudUsageGating(t *testing.T) {
	// Isolate the config file so the saved-cookie probe reads a temp file,
	// never the developer's real ~/.config/evilcode.
	writeLoginConfig(t)
	t.Setenv(config.EnvOllamaSessionCookie, "")
	m := NewModel(nil, HeaderState{})
	now := time.Now()

	if m.maybeRefreshCloudUsage(now) {
		t.Error("no cookie configured: must not fetch")
	}

	t.Setenv("OLLAMA_SESSION_COOKIE", "session-value")
	if !m.maybeRefreshCloudUsage(now) {
		t.Error("cookie configured, never fetched: should fetch now")
	}

	m.cloudUsagePending = true
	if m.maybeRefreshCloudUsage(now) {
		t.Error("pending fetch: must not start another")
	}
	m.cloudUsagePending = false

	m.cloudUsageNext = now.Add(time.Minute)
	if m.maybeRefreshCloudUsage(now) {
		t.Error("before the refresh window: must not fetch")
	}
	m.cloudUsageNext = now.Add(-time.Minute)
	if !m.maybeRefreshCloudUsage(now) {
		t.Error("past the refresh window: should fetch")
	}

	// A cookie saved by /connect ollama-usage (no env var) also arms the
	// widget. Clear the memo so the file is re-probed.
	t.Setenv("OLLAMA_SESSION_COOKIE", "")
	m.cloudUsageCookieValue = ""
	m.cloudUsageCookieAt = time.Time{}
	m.cloudUsageNext = time.Time{}
	if err := config.SaveOllamaSessionCookie("saved-secret"); err != nil {
		t.Fatal(err)
	}
	if !m.maybeRefreshCloudUsage(now) {
		t.Error("cookie saved via /connect: should fetch")
	}

	// Deterministic (golden) mode never touches the network, even with a cookie.
	t.Setenv("EVILCODE_DETERMINISTIC", "1")
	m.cloudUsageNext = time.Time{}
	if m.maybeRefreshCloudUsage(now) {
		t.Error("deterministic mode must never fetch")
	}
}
