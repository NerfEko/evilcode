package agent

import (
	"context"
	"sync"
	"testing"

	"evilcode/internal/provider"
)

// imageCaptureProvider records every request it receives and answers with a
// short text turn, so image tests can assert what the provider actually saw.
type imageCaptureProvider struct {
	mu   sync.Mutex
	reqs []provider.Req
}

func (p *imageCaptureProvider) Name() string { return "image-capture" }

func (p *imageCaptureProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (p *imageCaptureProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *imageCaptureProvider) ChatStream(_ context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Text: "ok"}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

func (p *imageCaptureProvider) requests() []provider.Req {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Req(nil), p.reqs...)
}

func (p *imageCaptureProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

func findUserMessage(msgs []provider.Message, idx int) *provider.Message {
	n := 0
	for i := range msgs {
		if msgs[i].Role == provider.RoleUser {
			if n == idx {
				return &msgs[i]
			}
			n++
		}
	}
	return nil
}

// TestImageOnlyTurnCarriesImages proves an image-only turn reaches the
// provider with its images instead of being dropped.
func TestImageOnlyTurnCarriesImages(t *testing.T) {
	p := &imageCaptureProvider{}
	a := newTestAgent(t, p, nil)
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	a.Attach([][]byte{img})

	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "") }); err != nil {
		t.Fatal(err)
	}

	msgs := a.Conv.Messages()
	got := findUserMessage(msgs, 0)
	if got == nil {
		t.Fatalf("no user message appended for image-only turn: %v", msgs)
	}
	if len(got.Images) != 1 || string(got.Images[0]) != string(img) {
		t.Fatalf("user message images = %d, want 1 attached image", len(got.Images))
	}

	reqs := p.requests()
	if len(reqs) == 0 {
		t.Fatal("provider was never called for the image-only turn")
	}
	var sawImage bool
	for _, m := range reqs[0].Messages {
		if m.Role == provider.RoleUser && len(m.Images) == 1 {
			sawImage = true
		}
	}
	if !sawImage {
		t.Errorf("provider request carried no user images: %+v", reqs[0].Messages)
	}
}

// TestImageOnlyDoesNotLeakIntoNextTurn proves staging is cleared by the
// image-only turn, so the following text turn carries no images.
func TestImageOnlyDoesNotLeakIntoNextTurn(t *testing.T) {
	p := &imageCaptureProvider{}
	a := newTestAgent(t, p, nil)
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	a.Attach([][]byte{img})

	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "") }); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "next") }); err != nil {
		t.Fatal(err)
	}

	msgs := a.Conv.Messages()
	second := findUserMessage(msgs, 1)
	if second == nil {
		t.Fatalf("second user message missing: %v", msgs)
	}
	if len(second.Images) != 0 {
		t.Errorf("second turn leaked %d images, want 0", len(second.Images))
	}
	if second.Content != "next" {
		t.Errorf("second turn content = %q, want %q", second.Content, "next")
	}
}

// TestImageOnlyPreservesHidden proves the hidden marker survives an
// image-only turn.
func TestImageOnlyPreservesHidden(t *testing.T) {
	p := &imageCaptureProvider{}
	a := newTestAgent(t, p, nil)
	a.Attach([][]byte{[]byte("raw-bytes")})

	if _, err := collect(t, a, func() error { return a.RunHidden(context.Background(), "") }); err != nil {
		t.Fatal(err)
	}
	msgs := a.Conv.Messages()
	got := findUserMessage(msgs, 0)
	if got == nil {
		t.Fatalf("no user message appended for hidden image-only turn: %v", msgs)
	}
	if !got.Hidden {
		t.Error("hidden image-only message lost its Hidden marker")
	}
	if len(got.Images) != 1 {
		t.Errorf("hidden image-only message has %d images, want 1", len(got.Images))
	}
}

// TestImageOnlySkipsRecall proves recall runs for text turns but not for
// image-only turns.
func TestImageOnlySkipsRecall(t *testing.T) {
	p := &imageCaptureProvider{}
	a := newTestAgent(t, p, nil)
	var mu sync.Mutex
	var recallCalls []string
	a.Recall = func(_ context.Context, input string) (string, any) {
		mu.Lock()
		recallCalls = append(recallCalls, input)
		mu.Unlock()
		return "", nil
	}

	a.Attach([][]byte{[]byte("img")})
	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "") }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	nAfterImageOnly := len(recallCalls)
	mu.Unlock()
	if nAfterImageOnly != 0 {
		t.Errorf("recall ran %d times for image-only turn, want 0", nAfterImageOnly)
	}

	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "hello") }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	nAfterText := len(recallCalls)
	mu.Unlock()
	if nAfterText != 1 {
		t.Errorf("recall ran %d times after text turn, want 1", nAfterText)
	}
}

// TestAttachWhitespaceWithoutImagesIsNoop proves a whitespace-only prompt
// with no staged images commits no message and makes no provider call.
func TestAttachWhitespaceWithoutImagesIsNoop(t *testing.T) {
	p := &imageCaptureProvider{}
	a := newTestAgent(t, p, nil)

	if err := a.Run(context.Background(), "   "); err != nil {
		t.Fatal(err)
	}
	if got := a.Conv.Len(); got != 0 {
		t.Errorf("conversation length = %d, want 0 (no phantom turn)", got)
	}
	if n := p.calls(); n != 0 {
		t.Errorf("provider calls = %d, want 0 (no phantom turn)", n)
	}

	// Empty string behaves the same way.
	if err := a.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := a.Conv.Len(); got != 0 {
		t.Errorf("conversation length = %d, want 0 (no phantom turn)", got)
	}
	if n := p.calls(); n != 0 {
		t.Errorf("provider calls = %d, want 0 (no phantom turn)", n)
	}
}

// TestAttachImageOnlyPersists proves the image-only user message reaches the
// persistence sink with its bytes intact.
func TestAttachImageOnlyPersists(t *testing.T) {
	p := &imageCaptureProvider{}
	a := newTestAgent(t, p, nil)
	var mu sync.Mutex
	var persisted []provider.Message
	a.Conv.Persist(func(m provider.Message) error {
		mu.Lock()
		persisted = append(persisted, m)
		mu.Unlock()
		return nil
	})

	img := []byte{0x89, 0x50, 0x4e, 0x47}
	a.Attach([][]byte{img})
	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "") }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, m := range persisted {
		if m.Role == provider.RoleUser && len(m.Images) == 1 && string(m.Images[0]) == string(img) {
			found = true
		}
	}
	if !found {
		t.Errorf("image-only user message was not persisted with images: %+v", persisted)
	}
}
