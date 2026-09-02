package daemon

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// webClient disables the transport's transparent gzip so tests see exactly
// what the server sent, the way a browser would before it decodes.
var webClient = &http.Client{Transport: &http.Transport{DisableCompression: true}}

// authedWebGet fetches a web path with the server's own token as a bearer and
// decodes any Content-Encoding the server applied.
func authedWebGet(t *testing.T, srv *Server, addr, path string) *http.Response {
	t.Helper()
	tok, _, err := loadOrMintWebToken(srv.webTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := webClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("GET %s: advertised gzip is not gzip: %v", path, err)
		}
		body, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("GET %s: gunzip: %v", path, err)
		}
		resp.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	return resp
}

func TestWebIndexServesShell(t *testing.T) {
	srv, addr := webTestServer(t)

	resp := authedWebGet(t, srv, addr, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, `<div id="app">`) {
		t.Error("the shell is missing its mount point")
	}
	if !strings.Contains(html, `rel="manifest" href="/manifest.webmanifest"`) {
		t.Error("the shell does not link the PWA manifest")
	}
	if !strings.Contains(html, `<meta name="theme-color" content="#414559">`) {
		t.Errorf("theme-color meta is missing or not palette-derived:\n%s", html)
	}
	if !strings.Contains(html, `href="/theme.css"`) {
		t.Error("the shell does not load the generated theme tokens")
	}
	if !strings.Contains(html, `viewport-fit=cover`) {
		t.Error("the shell is missing the phone viewport meta (§9)")
	}
}

func TestWebSecurityHeadersOnEveryResponse(t *testing.T) {
	srv, addr := webTestServer(t)

	for _, path := range []string{"/", "/theme.css", "/assets/css/app.css", "/definitely/not/here"} {
		resp := authedWebGet(t, srv, addr, path)
		if got := resp.Header.Get("Content-Security-Policy"); got != webCSP {
			t.Errorf("%s: CSP = %q", path, got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", path, got)
		}
		if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", path, got)
		}
	}
}

func TestWebAssetsAreServedNoStore(t *testing.T) {
	srv, addr := webTestServer(t)

	for _, path := range []string{
		"/assets/css/app.css",
		"/assets/js/app.js",
		"/assets/icons/icon-192.png",
		"/assets/icons/icon-512.png",
		"/assets/icons/apple-touch-icon.png",
	} {
		resp := authedWebGet(t, srv, addr, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, got)
		}
	}
	// The icons must actually be PNGs, not a 404 page that happens to exist.
	resp := authedWebGet(t, srv, addr, "/assets/icons/icon-192.png")
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("icon Content-Type = %q, want image/png", ct)
	}
}

func TestWebThemeCSSEndpoint(t *testing.T) {
	srv, addr := webTestServer(t)

	resp := authedWebGet(t, srv, addr, "/theme.css")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /theme.css: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	css := string(body)
	for _, token := range []string{"--user:", "--user-text:", "--prose-h1:", "--diff-add:", "--diff-del:"} {
		if !strings.Contains(css, token) {
			t.Errorf("/theme.css is missing %s", token)
		}
	}
	// The endpoint must serve the configured palette, not a hardcoded one.
	srv.Cfg.Display.Theme = "dracula"
	resp2 := authedWebGet(t, srv, addr, "/theme.css")
	body2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body2), "--user: #bd93f9") {
		t.Errorf("theme swap did not follow the config:\n%s", body2)
	}
	srv.Cfg.Display.Theme = "catppuccin-frappe"
}

func TestWebManifestEndpoint(t *testing.T) {
	srv, addr := webTestServer(t)

	resp := authedWebGet(t, srv, addr, "/manifest.webmanifest")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /manifest.webmanifest: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var manifest struct {
		Name            string `json:"name"`
		Display         string `json:"display"`
		ThemeColor      string `json:"theme_color"`
		BackgroundColor string `json:"background_color"`
		Icons           []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
			Type  string `json:"type"`
		} `json:"icons"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest.Name != "evilcode" || manifest.Display != "standalone" {
		t.Errorf("manifest identity = %q display = %q", manifest.Name, manifest.Display)
	}
	if manifest.ThemeColor != "#414559" || manifest.BackgroundColor != "#414559" {
		t.Errorf("manifest colors = theme %q background %q, want the palette surface", manifest.ThemeColor, manifest.BackgroundColor)
	}
	if len(manifest.Icons) != 2 {
		t.Errorf("manifest carries %d icons, want the 192 and 512 pair", len(manifest.Icons))
	}
}
