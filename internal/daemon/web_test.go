package daemon

import (
	"errors"
	"io"
	"net"
	"net/http"
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

	resp := webGet(t, "http://"+addr+"/")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET / before routes exist: status %d, want 404", resp.StatusCode)
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

// The mux is the only thing reachable on the web port: a request that is not a
// registered route must fall through to 404 rather than panic or echo.
func TestWebMuxUnknownPathIs404(t *testing.T) {
	_, addr := webTestServer(t)
	resp := webGet(t, "http://"+addr+"/definitely/not/here")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: status %d, want 404", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Error("404 with an empty body gives a browser nothing to render")
	}
}
