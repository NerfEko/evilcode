package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evilcode/internal/config"
)

// stubMux is a stand-in for the real routes: it 200s everything that reaches
// it, so a test asserting 401/403 knows the auth wrapper produced the status.
func stubMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	return mux
}

// authTestServer wraps a stub mux with webAuth bound to token and addr.
func authTestServer(t *testing.T, token, addr string) *httptest.Server {
	t.Helper()
	a := &webAuth{token: token, addr: addr, requireAuth: true}
	srv := httptest.NewServer(a.wrap(stubMux()))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebTokenMintAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock.web-token")

	tok, minted, err := loadOrMintWebToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if !minted {
		t.Error("first load must mint")
	}
	if err := validWebToken(tok); err != nil {
		t.Fatalf("minted token invalid: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("minted token file mode = %v, want 0600", info.Mode().Perm())
	}

	// Reload: reused, never re-minted, identical value.
	again, mintedAgain, err := loadOrMintWebToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if mintedAgain {
		t.Error("second load re-minted the token")
	}
	if again != tok {
		t.Error("the token changed across loads")
	}

	// A loose file mode is re-secured on load (backup-restore case).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrMintWebToken(path); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("loaded token file mode = %v, want re-chmod to 0600", info.Mode().Perm())
	}
}

func TestWebTokenCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock.web-token")
	if err := os.WriteFile(path, []byte("not-a-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrMintWebToken(path); err == nil {
		t.Fatal("a corrupt token file must be a hard error, not a silent rotation")
	}
}

func TestWebTokenSurvivesRestart(t *testing.T) {
	// Two listener lifetimes over one token file: the second start reuses the
	// file, which is what keeps a browser's cookie valid across daemon
	// restarts (decision 2).
	path := filepath.Join(t.TempDir(), "s.sock.web-token")
	first, _, err := loadOrMintWebToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, minted, err := loadOrMintWebToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if minted || first != second {
		t.Fatalf("restart rotated the token: %q -> %q (minted=%v)", first, second, minted)
	}
}

func TestWebAuthRequiresToken(t *testing.T) {
	addr := "127.0.0.1:7749"
	srv := authTestServer(t, strings.Repeat("a", 64), addr)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", resp.StatusCode)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, `"error"`) {
		t.Errorf("401 body %q is not the uniform error shape", body)
	}
}

func TestWebAuthAcceptsCookieAndBearer(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	addr := "127.0.0.1:7749"
	srv := authTestServer(t, tok, addr)

	// Cookie flow: handoff sets the cookie, the redirect target works.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(srv.URL + "/?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("handoff: status %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("handoff redirected to %q, want \"/\" (token stripped)", loc)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Value != tok {
		t.Fatalf("handoff cookies = %+v, want exactly the token cookie", cookies)
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 {
		t.Errorf("cookie flags wrong: %+v", c)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(c)
	if resp, err := client.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("cookie-authenticated GET: status %d, want 200", resp.StatusCode)
		}
	}

	// Bearer flow, no cookie.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bearer-authenticated GET: status %d, want 200", resp.StatusCode)
	}

	// Wrong secret in either channel is still a 401.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("ff", 32))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong bearer: status %d, want 401", resp.StatusCode)
	}
}

func TestWebAuthHandoffRejectsWrongToken(t *testing.T) {
	srv := authTestServer(t, strings.Repeat("a", 64), "127.0.0.1:7749")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(srv.URL + "/?token=" + strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong handoff token: status %d, want 401", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Error("a failed handoff must not set any cookie")
	}
}

func TestWebAuthMutatingVerbsRequireSameOrigin(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	addr := "127.0.0.1:7749"
	srv := authTestServer(t, tok, addr)
	post := func(host, origin, referer string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/input", strings.NewReader(`{}`))
		req.Host = host
		req.AddCookie(&http.Cookie{Name: webCookieName, Value: tok})
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// No Origin/Referer at all: unverifiable, rejected.
	if got := post("127.0.0.1:7749", "", ""); got != http.StatusForbidden {
		t.Errorf("POST without Origin: status %d, want 403", got)
	}
	// Cross-origin page: rejected.
	if got := post("127.0.0.1:7749", "https://evil.example", ""); got != http.StatusForbidden {
		t.Errorf("cross-origin POST: status %d, want 403", got)
	}
	// Correct origin on the bound host: allowed.
	if got := post("127.0.0.1:7749", "http://127.0.0.1:7749", ""); got != http.StatusOK {
		t.Errorf("same-origin POST: status %d, want 200", got)
	}
	// localhost spelling of the bound port: allowed.
	if got := post("localhost:7749", "http://localhost:7749", ""); got != http.StatusOK {
		t.Errorf("localhost POST: status %d, want 200", got)
	}
	// Referer fallback (Safari strips Origin on some redirects): allowed.
	if got := post("127.0.0.1:7749", "", "http://127.0.0.1:7749/settings"); got != http.StatusOK {
		t.Errorf("Referer-only POST: status %d, want 200", got)
	}
}

func TestWebAuthAcceptsHTTPSForwardedOrigin(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	srv := authTestServer(t, tok, "127.0.0.1:7749")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/input", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:7749"
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: tok})
	req.Header.Set("X-Forwarded-Host", "gentoo.tail9da06.ts.net")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://gentoo.tail9da06.ts.net")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HTTPS forwarded same-origin POST: status %d, want 200", resp.StatusCode)
	}
}

func TestWebAuthRejectsRebindingHost(t *testing.T) {
	tok := strings.Repeat("ab", 32)
	srv := authTestServer(t, tok, "127.0.0.1:7749")

	// The attacker resolves attacker.example to 127.0.0.1 and posts from a
	// page on http://attacker.example:7749. Host is not in the allowlist, so
	// this is 403 even though the socket-level destination is loopback.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/input", strings.NewReader(`{}`))
	req.Host = "attacker.example:7749"
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: tok})
	req.Header.Set("Origin", "http://attacker.example:7749")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebinding Host POST: status %d, want 403", resp.StatusCode)
	}

	// The same request with a GET would be authenticated but never mutating;
	// the rebinding rule must not depend on the verb alone.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/input", strings.NewReader(`{}`))
	req.Host = "attacker.example"
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: tok})
	req.Header.Set("Origin", "http://attacker.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("portless rebinding Host POST: status %d, want 403", resp.StatusCode)
	}
}

func TestWebAuthOriginHostMustMatchHostHeader(t *testing.T) {
	// A legal Host with a foreign Origin is still a cross-site POST.
	tok := strings.Repeat("ab", 32)
	srv := authTestServer(t, tok, "127.0.0.1:7749")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/input", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:7749"
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: tok})
	req.Header.Set("Origin", "http://localhost:7749")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Origin/Host mismatch: status %d, want 403", resp.StatusCode)
	}
}

// readAll is the test-side twin of webError's body reader.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

func TestWebInfoMintProvenance(t *testing.T) {
	srv, path := testServer(t)
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	info := srv.WebInfo()
	if info == nil {
		t.Fatal("the web surface is on but WebInfo() is nil")
	}
	if !info.Minted {
		t.Error("the first start must report minted=true")
	}
	if info.TokenPath != path+".web-token" {
		t.Errorf("TokenPath = %q, want the file beside the socket", info.TokenPath)
	}
	if info.Addr == "" || info.Token == "" {
		t.Fatalf("WebInfo is incomplete: %+v", info)
	}
	srv.Close()

	// A second lifetime over the same socket path reuses the token file: the
	// tokenized URL must not print again, and the cookie keeps working.
	cfg, err := config.Load() // testServer's env is still in effect
	if err != nil {
		t.Fatal(err)
	}
	srv2 := NewServer(cfg, srv.Cwd, "")
	srv2.Path = path
	if err := srv2.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv2.Close)
	if err := srv2.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	info2 := srv2.WebInfo()
	if info2 == nil {
		t.Fatal("the restarted daemon reports no web surface")
	}
	if info2.Minted {
		t.Error("a restart must not report minted — the tokenized URL prints once")
	}
	if info2.Token != info.Token {
		t.Error("the restart rotated the token, which would break every browser cookie")
	}
}

func TestWebInfoNilWhenWebOff(t *testing.T) {
	srv, _ := testServer(t)
	if info := srv.WebInfo(); info != nil {
		t.Errorf("WebInfo with no web listener = %+v, want nil", info)
	}
}
