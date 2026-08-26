// Package cloudusage reads Ollama Cloud quota usage from the authenticated
// settings page.
//
// Ollama Cloud exposes no usage API (ollama/ollama#12532, ollama/ollama#15663):
// the session (5h) and weekly windows are server-rendered into the HTML of
// https://ollama.com/settings, which is reachable with the browser's session
// cookie (`__Secure-session`) and nothing else. This package fetches that page
// and parses the usage tracks out of it.
//
// The page structure is treated as a scrape target, not a contract: every
// attribute below is what the page served as of 2026-08, and the parser
// degrades (empty windows, skipped segments) rather than panicking when the
// markup shifts.
package cloudusage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// SettingsURL is where the Cloud Usage section of the account page lives.
const SettingsURL = "https://ollama.com/settings"

// Errors the widget maps to user-facing messages.
var (
	// ErrNoCookie means no session cookie was configured.
	ErrNoCookie = errors.New("cloudusage: no session cookie configured")
	// ErrNotLoggedIn means the settings page bounced to a sign-in screen: the
	// cookie is missing, expired, or revoked.
	ErrNotLoggedIn = errors.New("cloudusage: session expired or invalid")
	// ErrNoUsageData means the page loaded but carries no usage markers — the
	// markup changed or the account has no cloud usage. Distinct from a login
	// failure so the widget can say which.
	ErrNoUsageData = errors.New("cloudusage: no usage data found on page")
)

// Segment is one model's slice of a quota window, as rendered by the page.
type Segment struct {
	// Model is the model name, e.g. "deepseek-v4-flash:0731".
	Model string
	// Requests is how many requests the window's usage counts for the model.
	Requests int
	// ColorHex is the #rrggbb Ollama paints the segment with, or "" when the
	// page gave none.
	ColorHex string
	// WidthPct is the segment's share of the window's request count, 0..100.
	WidthPct float64
}

// Window is one quota window ("Session", "Hourly", "Weekly") and its bar.
type Window struct {
	// Label is the window's name as rendered on the page, e.g. "Session".
	Label string
	// UsedPercent is the quota consumed, 0..100.
	UsedPercent float64
	// ResetsAt is when the window rolls over; zero when the page gave none.
	ResetsAt time.Time
	// Segments holds the per-model slices of this window's bar.
	Segments []Segment
}

// Snapshot is a parsed copy of the settings page's Cloud Usage section.
type Snapshot struct {
	// Plan is the account tier ("Free", "Pro", ...), "" when absent.
	Plan string
	// Windows holds each usage track in page order.
	Windows []Window
	// FetchedAt is when the page was retrieved.
	FetchedAt time.Time
}

// Requests returns the total request count across the window's segments.
func (w Window) Requests() int {
	n := 0
	for _, s := range w.Segments {
		n += s.Requests
	}
	return n
}

// Fetch retrieves and parses the Ollama Cloud settings page.
//
// cookie is the browser session: either the bare `__Secure-session` value or a
// full Cookie header line. pageURL defaults to SettingsURL when empty. On
// failure the error distinguishes ErrNoCookie, ErrNotLoggedIn, and
// ErrNoUsageData.
func Fetch(ctx context.Context, cookie, pageURL string, now time.Time) (Snapshot, error) {
	if strings.TrimSpace(cookie) == "" {
		return Snapshot{}, ErrNoCookie
	}
	if pageURL == "" {
		pageURL = SettingsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("cloudusage: building request: %w", err)
	}
	req.Header.Set("Cookie", cookieHeader(cookie))
	req.Header.Set("User-Agent", "evilcode/1.0 (ollama cloud usage widget)")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("cloudusage: fetching %s: %w", pageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return Snapshot{}, fmt.Errorf("cloudusage: %s (rate limited)", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		if sessionBounced(resp) {
			return Snapshot{}, ErrNotLoggedIn
		}
		return Snapshot{}, fmt.Errorf("cloudusage: %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Snapshot{}, fmt.Errorf("cloudusage: reading response: %w", err)
	}
	if sessionBounced(resp) || looksSignedOut(body) {
		return Snapshot{}, ErrNotLoggedIn
	}

	snap, ok := ParseBody(string(body), now)
	if !ok {
		return Snapshot{}, ErrNoUsageData
	}
	return snap, nil
}

// cookieHeader turns the configured value into a Cookie header. A value that
// already looks like a header (contains '=') is passed through, so pasting the
// browser's whole Cookie line works; a bare value is treated as the
// `__Secure-session` cookie the settings page is served under.
func cookieHeader(cookie string) string {
	if strings.Contains(cookie, "=") {
		return cookie
	}
	return "__Secure-session=" + cookie
}

// sessionBounced reports whether the response's final URL is a sign-in route,
// which is where an expired session lands instead of the settings page.
func sessionBounced(resp *http.Response) bool {
	path := resp.Request.URL.Path
	if path == "" {
		if parsed, err := url.Parse(resp.Request.URL.String()); err == nil {
			path = parsed.Path
		}
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, "signin") || strings.Contains(lower, "/auth") ||
		strings.Contains(lower, "/login")
}

// looksSignedOut sniffs a body that reached the client for the markers a login
// page carries. The settings page itself never contains them.
func looksSignedOut(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"sign in to ollama", "log in to ollama",
		"/api/auth/signin", "authkit", "href=\"/login\"", "href='/login'",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var (
	trackLabelRe = regexp.MustCompile(`(?i)^(.+?)\s+usage\s+([\d.]+)%\s*used\s*$`)
	widthPctRe   = regexp.MustCompile(`width:\s*([\d.]+)\s*%`)
	backgroundRe = regexp.MustCompile(`background(?:-color)?:\s*(#[0-9a-fA-F]{6})`)
	planRe       = regexp.MustCompile(`(?is)Cloud Usage\s*</span>\s*<span[^>]*>\s*([^<]+?)\s*</span>`)
)

// ParseBody extracts the usage windows from the settings page's raw HTML.
// ok is false when the page carried no usage markers at all.
func ParseBody(htmlBody string, now time.Time) (Snapshot, bool) {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return Snapshot{}, false
	}

	snap := Snapshot{FetchedAt: now}
	if m := planRe.FindStringSubmatch(htmlBody); len(m) == 2 {
		snap.Plan = strings.TrimSpace(m[1])
	}

	walk(doc, func(n *html.Node) {
		if !hasAttr(n, "data-usage-track") {
			return
		}
		if w, ok := parseWindow(n); ok {
			snap.Windows = append(snap.Windows, w)
		}
	})
	if len(snap.Windows) == 0 {
		return Snapshot{}, false
	}
	return snap, true
}

// parseWindow reads one `data-usage-track` element: its aria-label carries the
// window label and used percent, its descendant `data-usage-segment` elements
// carry the per-model slices, and the nearest `data-usage-meter` sibling
// carries the reset time.
func parseWindow(track *html.Node) (Window, bool) {
	label, pct, ok := parseTrackLabel(attr(track, "aria-label"))
	if !ok {
		return Window{}, false
	}
	w := Window{Label: label, UsedPercent: pct}

	walk(track, func(n *html.Node) {
		if n == track || !hasAttr(n, "data-usage-segment") {
			return
		}
		seg := parseSegment(n)
		if seg.Model != "" {
			w.Segments = append(w.Segments, seg)
		}
	})

	if meter := ancestor(track, "data-usage-meter"); meter != nil {
		for sib := meter.NextSibling; sib != nil; sib = sib.NextSibling {
			if raw := attr(sib, "data-time"); raw != "" {
				if t, err := time.Parse(time.RFC3339, raw); err == nil {
					w.ResetsAt = t
				}
				break
			}
		}
	}
	return w, true
}

func parseTrackLabel(aria string) (label string, pct float64, ok bool) {
	m := trackLabelRe.FindStringSubmatch(aria)
	if len(m) != 3 {
		return "", 0, false
	}
	p, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return "", 0, false
	}
	return titleLabel(m[1]), p, true
}

// titleLabel normalizes the window name from the aria-label ("Session usage",
// "Hourly usage", "Weekly usage") to a display label.
func titleLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func parseSegment(n *html.Node) Segment {
	style := attr(n, "style")
	seg := Segment{
		Model:    strings.TrimSpace(attr(n, "data-model")),
		Requests: parseInt(attr(n, "data-requests")),
		ColorHex: normalizeHex(backgroundRe.FindStringSubmatch(style)),
	}
	if m := widthPctRe.FindStringSubmatch(style); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			seg.WidthPct = v
		}
	}
	return seg
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func normalizeHex(m []string) string {
	if len(m) != 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

// ancestor walks up from n looking for an element carrying the named attribute.
func ancestor(n *html.Node, attrName string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && hasAttr(p, attrName) {
			return p
		}
	}
	return nil
}

// hasAttr reports whether n carries the named attribute, whatever its
// value (boolean attributes like data-usage-track parse with an empty value).
func hasAttr(n *html.Node, name string) bool {
	for _, a := range n.Attr {
		if a.Key == name {
			return true
		}
	}
	return false
}

// attr returns the value of the named attribute, "" when absent.
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// walk visits n and every descendant in document order.
func walk(n *html.Node, fn func(*html.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}
