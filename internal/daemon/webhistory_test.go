package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// bigStoredSession writes a stored log with count user/assistant pairs.
func bigStoredSession(t *testing.T, name string, count int) {
	t.Helper()
	st, err := session.CreateNamedAt(config.DataDir(), name, "/tmp/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if err := st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("q%d", i)}); err != nil {
			t.Fatal(err)
		}
		if err := st.WriteMessage(provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("a%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func fetchPage(t *testing.T, srv *Server, addr, name, query string) webHistoryPage {
	t.Helper()
	resp := authedWebGet(t, srv, addr, "/api/sessions/"+name+"/messages"+query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET messages%s: status %d", query, resp.StatusCode)
	}
	var page webHistoryPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("page decode: %v", err)
	}
	return page
}

func TestWebHistoryPaginationWalksToMessageZero(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "longlog", 60) // 120 shaped messages

	page := fetchPage(t, srv, addr, "longlog", "?before=120")
	if len(page.Messages) != 50 || !page.HasMore || page.Oldest != 70 {
		t.Fatalf("first page = %d msgs, oldest %d, hasMore %v", len(page.Messages), page.Oldest, page.HasMore)
	}
	// Index 70 in the shaped list is the question of pair 35.
	if got := page.Messages[0].Content; got != "q35" {
		t.Errorf("first message of the last page = %q, want q35", got)
	}
	if last := page.Messages[len(page.Messages)-1].Content; last != "a59" {
		t.Errorf("last message = %q, want a59 (newest)", last)
	}

	// Walk up: each page's oldest is the next request's before, and every
	// page must tile the list exactly — oldest+len == the before we asked with.
	requested := 120
	for page.HasMore {
		next := page.Oldest
		page = fetchPage(t, srv, addr, "longlog", fmt.Sprintf("?before=%d", next))
		if got := page.Oldest + len(page.Messages); got != next {
			t.Fatalf("page seam broken: oldest %d + %d msgs != requested before %d", page.Oldest, len(page.Messages), next)
		}
		requested = page.Oldest
	}
	if requested != 0 {
		t.Errorf("walk stopped at %d, want message 0", requested)
	}
	// The exclusive bound: before=0 names the oldest message, so the page
	// above it is empty.
	if top := fetchPage(t, srv, addr, "longlog", "?before=0"); len(top.Messages) != 0 || top.HasMore {
		t.Errorf("before=0: %d msgs hasMore %v, want an empty page", len(top.Messages), top.HasMore)
	}
}

func TestWebHistoryLimitCapsAndDefaults(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "pagelimits", 150) // 300 shaped messages

	// Default limit is 50; an omitted before means the newest page.
	page := fetchPage(t, srv, addr, "pagelimits", "")
	if len(page.Messages) != 50 || page.Oldest != 250 {
		t.Errorf("newest page = %d msgs oldest %d, want 50 from 250", len(page.Messages), page.Oldest)
	}
	// The cap is 200 even when the client asks for thousands.
	page = fetchPage(t, srv, addr, "pagelimits", "?before=300&limit=100000")
	if len(page.Messages) != 200 {
		t.Errorf("limit=100000 returned %d msgs, want the 200 cap", len(page.Messages))
	}
	// A beyond-the-end before clamps to the newest page instead of 404-ing.
	page = fetchPage(t, srv, addr, "pagelimits", "?before=99999&limit=5")
	if len(page.Messages) != 5 || page.Messages[4].Content != "a149" {
		t.Errorf("clamped page wrong: %d msgs, last %q", len(page.Messages), page.Messages[len(page.Messages)-1].Content)
	}
	// Garbage and negative values degrade to the default, not an error: the
	// malformed before degrades to 0, and the page above message 0 is empty.
	page = fetchPage(t, srv, addr, "pagelimits", "?limit=potato&before=-7")
	if len(page.Messages) != 0 || page.HasMore {
		t.Errorf("malformed query: %d msgs hasMore %v, want the empty page above 0", len(page.Messages), page.HasMore)
	}
}

func TestWebHistoryTornReadRetriesOnce(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "torn", 3)

	// First read fails the way a read that caught the log mid-append or
	// mid-rewrite can; the retry must recover it. The loader seam keeps this
	// deterministic — the real loader's own tail repair is covered by the
	// torn-tail test below.
	var calls atomic.Int32
	restore := loadSessionMessages
	loadSessionMessages = func(path string) ([]provider.Message, error) {
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("simulated torn read")
		}
		return restore(path)
	}
	t.Cleanup(func() { loadSessionMessages = restore })

	page := fetchPage(t, srv, addr, "torn", "?before=99")
	if got := len(page.Messages); got != 6 {
		t.Errorf("retried read yielded %d messages, want the 6 complete ones", got)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("loader invoked %d times, want at least the retry", got)
	}
}

func TestWebHistoryTornTailIsTolerated(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "torn", 3)

	// A hard kill mid-append leaves a partial final line. The resume path
	// tolerates it; the web reader must too, because it is the same loader.
	path := session.Dir(config.DataDir()) + "/torn.jsonl"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","data":{"role":"user","cont`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	page := fetchPage(t, srv, addr, "torn", "?before=99")
	if got := len(page.Messages); got != 6 {
		t.Errorf("torn tail yielded %d messages, want the 6 complete ones", got)
	}
}

func TestWebHistorySurvivesAppendsRacingTheReader(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "racy", 10)

	// A live daemon appends to the log while readers page through it. The
	// reader must always succeed and never see a count that went backwards.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		st, err := session.Open(config.DataDir(), "racy")
		if err != nil {
			return
		}
		defer st.Close()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("late %d", i)})
		}
	}()
	defer wg.Wait()
	go func() {
		time.Sleep(80 * time.Millisecond)
		close(stop)
	}()

	last := -1
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Omitted before = the newest page, so the count grows as the appender
		// lands messages.
		resp := authedWebGet(t, srv, addr, "/api/sessions/racy/messages?limit=200")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("messages during appends: status %d", resp.StatusCode)
		}
		var page webHistoryPage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatalf("page decode during appends: %v", err)
		}
		n := page.Oldest + len(page.Messages)
		if n < last {
			t.Fatalf("history shrank from %d to %d during reads", last, n)
		}
		last = n
	}
	if last < 20 {
		t.Errorf("appender never visibly landed (last %d), the race was not exercised", last)
	}
}
