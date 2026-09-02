package daemon

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWebAPIStatus(t *testing.T) {
	srv, addr := webTestServer(t)

	resp := authedWebGet(t, srv, addr, "/api/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/status: status %d", resp.StatusCode)
	}
	var status ServerStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("status payload is not JSON: %v", err)
	}
	if status.PID != srv.Status().PID || status.Socket != srv.Path {
		t.Errorf("status = %+v, want the live daemon's own values", status)
	}
	if status.Web != addr {
		t.Errorf("status.Web = %q, want the bound address %q", status.Web, addr)
	}
}

func TestWebAPISessionsIsTheSocketRoster(t *testing.T) {
	srv, addr := webTestServer(t)

	// Empty daemon: the roster is an empty JSON array, not null.
	resp := authedWebGet(t, srv, addr, "/api/sessions")
	var empty []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&empty); err != nil {
		t.Fatalf("roster is not a JSON array: %v", err)
	}

	// A hydrated session must appear with the same shape `list` answers with.
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name := sess.Name
	resp = authedWebGet(t, srv, addr, "/api/sessions")
	var roster []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&roster); err != nil {
		t.Fatalf("roster decode: %v", err)
	}
	var found *SessionInfo
	for i := range roster {
		if roster[i].Name == name {
			found = &roster[i]
		}
	}
	if found == nil {
		t.Fatalf("roster %v is missing the live session", roster)
	}
	if !found.Live || !found.Stored {
		t.Errorf("alpha = live %v stored %v, want both", found.Live, found.Stored)
	}
	if found.Cwd != sess.Cwd {
		t.Errorf("roster cwd %q, want the session's %q", found.Cwd, sess.Cwd)
	}
	// A web subscriber counts toward Clients exactly like a TUI window.
	sub := sess.subscribe()
	resp = authedWebGet(t, srv, addr, "/api/sessions")
	if err := json.NewDecoder(resp.Body).Decode(&roster); err != nil {
		t.Fatal(err)
	}
	for i := range roster {
		if roster[i].Name == name && roster[i].Clients != 1 {
			t.Errorf("alpha clients = %d, want the subscription counted", roster[i].Clients)
		}
	}
	sess.unsubscribe(sub)
}

func TestWebAPIJSONIsGzipped(t *testing.T) {
	srv, addr := webTestServer(t)

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+webTokenOf(t, srv))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip for a JSON GET", got)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("body is not actually gzip: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "[") {
		t.Errorf("gunzipped body %q is not the roster", string(body))
	}

	// Without the client offering gzip the same endpoint streams plain JSON.
	req2, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/sessions", nil)
	req2.Header.Set("Authorization", "Bearer "+webTokenOf(t, srv))
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if got := resp2.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q with no Accept-Encoding, want identity", got)
	}
}

// webTokenOf reads the server's token for tests that build requests by hand.
func webTokenOf(t *testing.T, srv *Server) string {
	t.Helper()
	info := srv.WebInfo()
	if info == nil {
		t.Fatal("no web surface")
	}
	return info.Token
}
