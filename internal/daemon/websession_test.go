package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// storedSessionFixture writes a closed, stored session with count*2 messages
// and returns its name and log path.
func storedSessionFixture(t *testing.T, name string, count int) string {
	t.Helper()
	st, err := session.CreateNamedAt(config.DataDir(), name, "/tmp/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if err := st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("stored question %d", i)}); err != nil {
			t.Fatal(err)
		}
		if err := st.WriteMessage(provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("stored answer %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestWebAPILiveSessionIsASocketSnapshot(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	resp := authedWebGet(t, srv, addr, "/api/sessions/"+sess.Name)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET live session: status %d", resp.StatusCode)
	}
	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("snapshot decode: %v", err)
	}
	if snap.Session != sess.Name || snap.Cwd != sess.Cwd {
		t.Errorf("snapshot identity = %+v, want the live session's", snap)
	}
	// The web snapshot must be the same payload an attaching TUI receives.
	if want := sess.snapshot(); snap.Seq != want.Seq || len(snap.Messages) != len(want.Messages) {
		t.Errorf("web snapshot differs from the socket snapshot: %d/%d msgs, seq %d/%d",
			len(snap.Messages), len(want.Messages), snap.Seq, want.Seq)
	}
}

func TestWebAPIStoredSessionIsReadOnly(t *testing.T) {
	srv, addr := webTestServer(t)
	name := storedSessionFixture(t, "ghost", 3)

	resp := authedWebGet(t, srv, addr, "/api/sessions/"+name)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET stored session: status %d", resp.StatusCode)
	}
	var view StoredSession
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("stored view decode: %v", err)
	}
	if view.Session.Name != name || view.Session.Live || !view.Session.Stored {
		t.Errorf("stored metadata = %+v, want stored-not-live", view.Session)
	}
	if len(view.Messages) != 6 {
		t.Errorf("stored history has %d messages, want all 6", len(view.Messages))
	}
	if view.Truncated {
		t.Error("a 6-message log is not truncated")
	}
	// Viewing a stored session must not boot a runtime.
	srv.mu.Lock()
	_, live := srv.sessions[name]
	srv.mu.Unlock()
	if live {
		t.Error("viewing a stored session hydrated it; read-only views must not build agents")
	}
}

func TestWebAPIStoredSessionStripsImageBytes(t *testing.T) {
	srv, addr := webTestServer(t)
	name := "imaginary"
	st, err := session.CreateNamedAt(config.DataDir(), name, "/tmp/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 3<<20) // 3 MiB of image bytes
	if err := st.WriteMessage(provider.Message{
		Role: provider.RoleUser, Content: "look", Images: [][]byte{big},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	resp := authedWebGet(t, srv, addr, "/api/sessions/"+name)
	var view StoredSession
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 1 {
		t.Fatalf("history has %d messages, want 1", len(view.Messages))
	}
	if len(view.Messages[0].Images) != 0 {
		t.Error("history carries image bytes; it must render placeholders instead")
	}
	if view.Messages[0].ImageCount != 1 {
		t.Errorf("history image count = %d, want 1", view.Messages[0].ImageCount)
	}
}

func TestWebAPISessionNotFoundAndPathTricks(t *testing.T) {
	srv, addr := webTestServer(t)
	storedSessionFixture(t, "real", 1)

	for _, path := range []string{
		"/api/sessions/nosuch",
		"/api/sessions/..",
		"/api/sessions/..%2Fetc",
		"/api/sessions/%2E%2E%2Fescape",
		"/api/sessions/real%2Ftrailing",
	} {
		resp := authedWebGet(t, srv, addr, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"error"`) {
			t.Errorf("GET %s: body %q is not the uniform error shape", path, body)
		}
	}
	// The real session is still there after the trick attempts.
	resp := authedWebGet(t, srv, addr, "/api/sessions/real")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/sessions/real after tricks: status %d", resp.StatusCode)
	}
}
