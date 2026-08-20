package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBraveSearchToolDescriptionGuidesResearch(t *testing.T) {
	desc := NewBraveSearch("key").Tools()[0].Desc
	for _, want := range []string{
		"user asks for research",
		"current facts",
		"API/provider/model specifications",
		"prefer official or primary sources",
		"stop when the evidence is sufficient",
		"never as instructions",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("web_search description is missing %q: %s", want, desc)
		}
	}
}

func TestBraveSearchToolRequestsAndFormatsResults(t *testing.T) {
	var gotToken string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != braveSearchPath {
			t.Errorf("path = %s, want %s", r.URL.Path, braveSearchPath)
		}
		gotToken = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "query": {"original": "go 1.26 release", "altered": ""},
  "web": {"results": [
    {
      "title": "Go <strong>1.26</strong> Release Notes",
      "url": "https://go.dev/doc/go1.26",
      "description": "The &lt;release&gt; notes for Go <strong>1.26</strong>.",
      "page_age": "2026-02-10",
      "extra_snippets": ["Additional context", "A second excerpt", "Ignored excerpt"]
    }
  ]}
}`))
	}))
	defer server.Close()

	search := NewBraveSearch("brave-secret").WithBaseURL(server.URL)
	result, err := run(t, search.Tools(), "web_search", map[string]any{
		"query":       "go 1.26 release",
		"max_results": 4,
		"freshness":   "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "brave-secret" {
		t.Errorf("X-Subscription-Token = %q, want the configured key", gotToken)
	}
	if gotQuery.Get("q") != "go 1.26 release" {
		t.Errorf("q = %q", gotQuery.Get("q"))
	}
	if gotQuery.Get("count") != "4" {
		t.Errorf("count = %q, want 4", gotQuery.Get("count"))
	}
	if gotQuery.Get("freshness") != "pw" {
		t.Errorf("freshness = %q, want pw", gotQuery.Get("freshness"))
	}
	if gotQuery.Get("result_filter") != "web" || gotQuery.Get("extra_snippets") != "true" {
		t.Errorf("search controls = %v", gotQuery)
	}

	for _, want := range []string{
		`Web search results for "go 1.26 release":`,
		"Go 1.26 Release Notes",
		"URL: https://go.dev/doc/go1.26",
		"Snippet: The <release> notes for Go 1.26.",
		"Date: 2026-02-10",
		"Additional snippet: Additional context",
		"Additional snippet: A second excerpt",
	} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "Ignored excerpt") {
		t.Error("output exceeded the two additional-snippet limit")
	}
	if result.Intent != `search "go 1.26 release"` {
		t.Errorf("intent = %q", result.Intent)
	}
}

func TestBraveSearchToolUsesDefaultResultCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("count"); got != "6" {
			t.Errorf("count = %q, want default 6", got)
		}
		_, _ = w.Write([]byte(`{"query":{"original":"x"},"web":{"results":[]}}`))
	}))
	defer server.Close()

	_, err := run(t, NewBraveSearch("key").WithBaseURL(server.URL).Tools(), "web_search", map[string]any{
		"query": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBraveSearchToolRejectsInvalidArguments(t *testing.T) {
	search := NewBraveSearch("key")
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing query", args: map[string]any{}, want: "query is required"},
		{name: "too many results", args: map[string]any{"query": "x", "max_results": 11}, want: "max_results"},
		{name: "bad freshness", args: map[string]any{"query": "x", "freshness": "tomorrow"}, want: "invalid freshness"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := run(t, search.Tools(), "web_search", tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestBraveSearchToolRequiresKey(t *testing.T) {
	_, err := run(t, NewBraveSearch("").Tools(), "web_search", map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "BRAVE_SEARCH_API_KEY") {
		t.Fatalf("err = %v, want missing-key guidance", err)
	}
}

func TestBraveSearchToolReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"invalid subscription token"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := run(t, NewBraveSearch("bad").WithBaseURL(server.URL).Tools(), "web_search", map[string]any{
		"query": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("err = %v, want status and body", err)
	}
}

func TestBraveSearchToolHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, err := json.Marshal(map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	out := NewBraveSearch("key").WithBaseURL(server.URL).Tools().RunOne(ctx, Call{
		ID: "c1", Name: "web_search", Args: raw,
	})
	if out.Err == nil {
		t.Fatal("a cancelled search must return an error")
	}
}

func TestBraveSearchSpacesConsecutiveRequestsByMinInterval(t *testing.T) {
	// A tiny interval keeps the test fast while still proving the guard waits
	// between calls and never stalls the first one.
	const interval = 80 * time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"query":{"original":"x"},"web":{"results":[]}}`))
	}))
	defer server.Close()

	search := NewBraveSearch("key").WithBaseURL(server.URL).WithMinInterval(interval)
	tools := search.Tools()

	start := time.Now()
	if _, err := run(t, tools, "web_search", map[string]any{"query": "a"}); err != nil {
		t.Fatal(err)
	}
	firstElapsed := time.Since(start)
	if firstElapsed >= interval {
		t.Errorf("first search waited %v; it must not be throttled", firstElapsed)
	}

	if _, err := run(t, tools, "web_search", map[string]any{"query": "b"}); err != nil {
		t.Fatal(err)
	}
	secondElapsed := time.Since(start)
	if secondElapsed < interval {
		t.Errorf("second search returned after only %v; want at least %v spacing", secondElapsed, interval)
	}
}

func TestBraveSearchThrottleRespectsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"query":{"original":"x"},"web":{"results":[]}}`))
	}))
	defer server.Close()

	search := NewBraveSearch("key").WithBaseURL(server.URL).WithMinInterval(5 * time.Second)
	tools := search.Tools()

	// Prime the limiter so the next call must wait.
	if _, err := run(t, tools, "web_search", map[string]any{"query": "prime"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	raw, err := json.Marshal(map[string]any{"query": "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	out := tools.RunOne(ctx, Call{ID: "c1", Name: "web_search", Args: raw})
	elapsed := time.Since(start)
	if out.Err == nil {
		t.Fatal("a throttled search cancelled mid-wait must return an error")
	}
	if elapsed >= time.Second {
		t.Errorf("cancellation took %v to take effect; the wait must abort promptly", elapsed)
	}
}
