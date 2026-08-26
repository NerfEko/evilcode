package cloudusage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixtureSettings mirrors the real structure of https://ollama.com/settings
// (anonymized; values from the page as served 2026-08).
const fixtureSettings = `<!doctype html>
<html><body>
<main>
  <h2>Usage</h2>
  <div><span>Cloud Usage</span><span>Free</span></div>

  <div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Session usage</span>
      <span class="text-sm">2.2% used</span>
    </div>
    <div class="relative group" data-usage-meter>
      <div data-usage-bubble aria-hidden="true">
        <span data-usage-model></span>
        <span data-usage-requests></span>
      </div>
      <div class="relative h-3 overflow-hidden rounded-full" data-usage-track aria-label="Session usage 2.2% used">
        <div class="flex h-full overflow-hidden" style="width: 2.2%;">
          <button type="button" style="width: 5.7%; background: #3b82f6" data-usage-segment data-model="web search" data-requests="2" aria-label="web search: 2 requests"></button>
          <button type="button" style="width: 94.3%; background: #4f46e5" data-usage-segment data-model="deepseek-v4-flash:0731" data-requests="68" aria-label="deepseek-v4-flash:0731: 68 requests"></button>
        </div>
      </div>
    </div>
    <div class="text-xs local-time" data-time="2026-08-04T09:00:00Z">Resets in 14 minutes.</div>
  </div>

  <div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Weekly usage</span>
      <span class="text-sm">0.8% used</span>
    </div>
    <div class="relative group" data-usage-meter>
      <div data-usage-bubble aria-hidden="true"></div>
      <div class="relative h-3" data-usage-track aria-label="Weekly usage 0.8% used">
        <div class="flex" style="width: 0.8%">
          <button type="button" style="width: 43.7%; background: #3b82f6" data-usage-segment data-model="glm-5.2" data-requests="17" aria-label="glm-5.2: 17 requests"></button>
          <button type="button" style="width: 2.6%; background: #3b82f6" data-usage-segment data-model="web search" data-requests="2" aria-label="web search: 2 requests"></button>
          <button type="button" style="width: 53.7%; background: #4f46e5" data-usage-segment data-model="deepseek-v4-flash:0731" data-requests="84" aria-label="deepseek-v4-flash:0731: 84 requests"></button>
        </div>
      </div>
    </div>
    <div data-time="2026-08-04T12:00:00Z">Resets in 3 hours.</div>
  </div>
</main>
</body></html>`

func TestParseBodyFixture(t *testing.T) {
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	snap, ok := ParseBody(fixtureSettings, now)
	if !ok {
		t.Fatal("fixture should parse")
	}
	if snap.Plan != "Free" {
		t.Errorf("plan = %q, want Free", snap.Plan)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(snap.Windows))
	}

	sess := snap.Windows[0]
	if sess.Label != "Session" {
		t.Errorf("first window label = %q, want Session", sess.Label)
	}
	if sess.UsedPercent != 2.2 {
		t.Errorf("session used = %v, want 2.2", sess.UsedPercent)
	}
	wantReset := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	if !sess.ResetsAt.Equal(wantReset) {
		t.Errorf("session resets = %v, want %v", sess.ResetsAt, wantReset)
	}
	if len(sess.Segments) != 2 {
		t.Fatalf("session segments = %d, want 2", len(sess.Segments))
	}
	first := sess.Segments[0]
	if first.Model != "web search" || first.Requests != 2 || first.ColorHex != "#3b82f6" {
		t.Errorf("first segment = %+v", first)
	}
	if first.WidthPct != 5.7 {
		t.Errorf("first segment width = %v, want 5.7", first.WidthPct)
	}
	second := sess.Segments[1]
	if second.Model != "deepseek-v4-flash:0731" || second.Requests != 68 || second.ColorHex != "#4f46e5" {
		t.Errorf("second segment = %+v", second)
	}

	weekly := snap.Windows[1]
	if weekly.Label != "Weekly" || weekly.UsedPercent != 0.8 {
		t.Errorf("weekly window = %+v", weekly)
	}
	if len(weekly.Segments) != 3 || weekly.Segments[0].Model != "glm-5.2" {
		t.Errorf("weekly segments = %+v", weekly.Segments)
	}
}

func TestParseBodyDegrades(t *testing.T) {
	now := time.Now()
	if _, ok := ParseBody("<html><body>no usage here</body></html>", now); ok {
		t.Error("markerless page should not parse")
	}
	// A track with an unparseable aria-label is skipped, not fatal.
	broken := `<div data-usage-track aria-label="???"></div>`
	if _, ok := ParseBody(broken, now); ok {
		t.Error("unparseable track should yield no windows")
	}
}

func TestParseBodySegmentWithoutColor(t *testing.T) {
	html := `<div data-usage-track aria-label="Session usage 50% used">
		<div data-usage-segment data-model="model-x" data-requests="3" style="width: 100%"></div>
	</div>`
	snap, ok := ParseBody(html, time.Now())
	if !ok || len(snap.Windows) != 1 {
		t.Fatalf("expected one window, got %+v ok=%v", snap, ok)
	}
	seg := snap.Windows[0].Segments[0]
	if seg.ColorHex != "" || seg.Requests != 3 {
		t.Errorf("segment = %+v, want no color and 3 requests", seg)
	}
}

func TestCookieHeader(t *testing.T) {
	if got := cookieHeader("abc123"); got != "__Secure-session=abc123" {
		t.Errorf("bare cookie = %q", got)
	}
	full := "__Secure-session=a; wos-session=b; path=/"
	if got := cookieHeader(full); got != full {
		t.Errorf("full header should pass through, got %q", got)
	}
}

func TestFetchSendsCookieAndParses(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixtureSettings))
	}))
	defer srv.Close()

	snap, err := Fetch(context.Background(), "secret-session", srv.URL, time.Now())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotCookie != "__Secure-session=secret-session" {
		t.Errorf("cookie sent = %q", gotCookie)
	}
	if len(snap.Windows) != 2 {
		t.Errorf("windows = %d, want 2", len(snap.Windows))
	}
}

func TestFetchRedirectsToSignin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signin" {
			http.Redirect(w, r, "/signin?redirect=/settings", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`<html><body>Sign in to Ollama</body></html>`))
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), "stale-session", srv.URL, time.Now())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("err = %v, want ErrNotLoggedIn", err)
	}
}

func TestFetchSignInPageBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>Sign in to Ollama</h1><a href="/login">Log in</a></body></html>`))
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), "expired", srv.URL, time.Now())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("err = %v, want ErrNotLoggedIn", err)
	}
}

func TestFetchNoUsageData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>settings without usage markers</body></html>`))
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), "abc", srv.URL, time.Now())
	if !errors.Is(err, ErrNoUsageData) {
		t.Errorf("err = %v, want ErrNoUsageData", err)
	}
}

func TestFetchNoCookie(t *testing.T) {
	if _, err := Fetch(context.Background(), "", "", time.Now()); !errors.Is(err, ErrNoCookie) {
		t.Errorf("err = %v, want ErrNoCookie", err)
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), "abc", srv.URL, time.Now())
	if err == nil || errors.Is(err, ErrNotLoggedIn) || errors.Is(err, ErrNoUsageData) {
		t.Errorf("err = %v, want a plain HTTP error", err)
	}
}

func TestLooksSignedOut(t *testing.T) {
	for _, body := range []string{
		"Please sign in to ollama to continue",
		`<form href="/login">`,
		"href='/login'",
		"workos authkit authorization",
	} {
		if !looksSignedOut([]byte(strings.ToLower(body))) {
			t.Errorf("should detect sign-in body %q", body)
		}
	}
}
