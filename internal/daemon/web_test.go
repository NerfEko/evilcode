package daemon

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// webTestServer builds a server with the web surface already listening on an
// OS-picked loopback port. The unix socket is also live, which is what lets
// the independence tests hold one failure against the other.
func webTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, _ := testServer(t)
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatalf("ListenWeb: %v", err)
	}
	return srv, srv.webAddr()
}

func webGet(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestListenWebServesAndCloses(t *testing.T) {
	srv, addr := webTestServer(t)

	// No routes registered yet: authenticated requests reach the mux and get
	// 404, unauthenticated ones stop at the auth wrapper with 401.
	resp := webGet(t, "http://"+addr+"/")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET / unauthenticated: status %d, want 401", resp.StatusCode)
	}
	tok, _, err := loadOrMintWebToken(srv.webTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	authed.Body.Close()
	if authed.StatusCode != http.StatusNotFound {
		t.Errorf("GET / authenticated: status %d, want 404", authed.StatusCode)
	}

	srv.Close()
	if _, err := net.Dial("tcp", addr); err == nil {
		t.Error("Close left the web listener accepting connections")
	}
}

func TestListenWebRefusesSecondListener(t *testing.T) {
	srv, addr := webTestServer(t)

	err := srv.ListenWeb(addr)
	if err == nil {
		t.Fatal("a second ListenWeb on the same address must be refused")
	}
	if got := srv.webAddr(); got != addr {
		t.Errorf("webAddr after refused re-bind = %q, want %q", got, addr)
	}
}

func TestListenWebDefaultsToLoopback7749(t *testing.T) {
	// A daemon that never asks for the web UI must not open a port: webAddr
	// stays empty and Close has nothing to do on the web path.
	srv, _ := testServer(t)
	if got := srv.webAddr(); got != "" {
		t.Fatalf("web without ListenWeb reports addr %q, want \"\"", got)
	}
}

func TestWebBindFailureKeepsSocketWorking(t *testing.T) {
	srv, path := testServer(t)

	// Occupy a port with a plain listener so the web bind is refused.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	if err := srv.ListenWeb(blocker.Addr().String()); err == nil {
		t.Fatal("ListenWeb on an occupied port must fail")
	}

	// The socket path must not care: the daemon still accepts and answers.
	client, err := DialPath(path)
	if err != nil {
		t.Fatalf("socket unusable after web bind failure: %v", err)
	}
	defer client.Close()
	if _, err := client.Status(); err != nil {
		t.Fatalf("Status over the socket failed after web bind failure: %v", err)
	}
}

func TestListenWebAfterCloseIsRefused(t *testing.T) {
	srv, _ := testServer(t)
	srv.Close()
	if err := srv.ListenWeb("127.0.0.1:0"); !errors.Is(err, errServerClosed) {
		t.Errorf("ListenWeb after Close = %v, want the shutdown error", err)
	}
}

// The mux is the only thing reachable on the web port. Auth runs before
// routing: an unauthenticated request is 401 even for unknown paths, and an
// authenticated request that matches no route is 404.
func TestWebMuxUnknownPathIs404(t *testing.T) {
	srv, addr := webTestServer(t)
	resp := webGet(t, "http://"+addr+"/definitely/not/here")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown path unauthenticated: status %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"error"`) {
		t.Errorf("401 body %q is not the uniform error shape", body)
	}

	tok, _, err := loadOrMintWebToken(srv.webTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/definitely/not/here", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path authenticated: status %d, want 404", resp.StatusCode)
	}
}
