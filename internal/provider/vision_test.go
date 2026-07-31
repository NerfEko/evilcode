package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32))

func TestOllamaWantsBareBase64(t *testing.T) {
	// Ollama takes raw base64 with no data URI and no MIME type — the opposite
	// of OpenAI, which is exactly why Message.Images holds raw bytes and each
	// provider encodes at its own edge.
	got := toOllamaMessages([]Message{
		{Role: RoleUser, Content: "what is this?", Images: [][]byte{pngBytes}},
	})
	if len(got) != 1 || len(got[0].Images) != 1 {
		t.Fatalf("images = %+v", got)
	}
	img := got[0].Images[0]
	if strings.HasPrefix(img, "data:") {
		t.Errorf("ollama got a data URI: %q", img[:20])
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"images"`) {
		t.Errorf("images field missing from the wire body: %s", raw)
	}
}

func TestOpenAIWantsContentParts(t *testing.T) {
	got := toOAIMessages([]Message{
		{Role: RoleUser, Content: "what is this?", Images: [][]byte{pngBytes}},
	})
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"type":"text"`, `"type":"image_url"`, "data:image/png;base64,"} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestOpenAIKeepsABareStringWithoutImages(t *testing.T) {
	// Every text-only request must keep emitting a plain string. Switching
	// everything to content parts would change the shape of every call to serve
	// the rare one.
	got := toOAIMessages([]Message{{Role: RoleUser, Content: "hello"}})
	raw, _ := json.Marshal(got[0])
	if !strings.Contains(string(raw), `"content":"hello"`) {
		t.Errorf("text-only message is not a bare string: %s", raw)
	}
}

func TestDetectImageMIME(t *testing.T) {
	cases := map[string][]byte{
		"image/png":  pngBytes,
		"image/jpeg": {0xFF, 0xD8, 0xFF, 0xE0},
		"image/gif":  []byte("GIF89a...."),
		"image/webp": []byte("RIFF____WEBP"),
		"image/bmp":  []byte("BM......"),
	}
	for want, data := range cases {
		if got := DetectImageMIME(data); got != want {
			t.Errorf("DetectImageMIME(%q) = %s, want %s", data[:4], got, want)
		}
	}
	// Unknown falls back to PNG, which is what the render path produces and
	// what every vision endpoint accepts.
	if got := DetectImageMIME([]byte("nonsense")); got != "image/png" {
		t.Errorf("fallback = %s", got)
	}
}

func TestImagesRoundTripThroughJSON(t *testing.T) {
	// The session log marshals a whole Message; []byte base64s for free, so no
	// store change is needed for the field to survive a resume.
	in := Message{Role: RoleUser, Content: "look", Images: [][]byte{pngBytes}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 1 || string(out.Images[0]) != string(pngBytes) {
		t.Errorf("images did not round trip: %v", out.Images)
	}
}
