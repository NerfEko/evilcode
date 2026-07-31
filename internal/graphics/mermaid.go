package graphics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MermaidCommand is the external renderer. plan.md §5 is explicit that evilcode
// does not write a diagram renderer: mermaid is a large, moving spec, and a
// half-implementation would be worse than an honest "install mmdc".
const MermaidCommand = "mmdc"

// MermaidTimeout bounds a render. mmdc starts a headless browser, so it is slow
// on a cold cache and can hang outright when its sandbox is unhappy — and a
// hung diagram must not take the turn with it.
const MermaidTimeout = 30 * time.Second

// MermaidAvailable reports whether the renderer is installed.
func MermaidAvailable() bool {
	_, err := exec.LookPath(MermaidCommand)
	return err == nil
}

// MermaidHint is what a code block shows when mmdc is absent.
//
// The source is still displayed with syntax highlighting; this is the line
// under it. It names the command rather than saying "unavailable", because the
// only useful thing to tell someone is what to install.
const MermaidHint = "↻ mermaid (render requires mmdc)"

// mermaidCache keeps rendered PNGs for the session. A diagram is re-rendered on
// every frame otherwise, and each render is a browser launch.
var mermaidCache struct {
	mu   sync.Mutex
	byID map[string][]byte
}

// MermaidKey is the cache key for a diagram's source.
func MermaidKey(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:8])
}

// RenderMermaid turns diagram source into a PNG, caching by content.
//
// A failure is returned rather than logged: the caller falls back to showing
// the source, which is a perfectly good outcome and not worth a notice every
// time someone writes a diagram this renderer dislikes.
func RenderMermaid(ctx context.Context, src string) ([]byte, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, fmt.Errorf("empty diagram")
	}
	key := MermaidKey(src)

	mermaidCache.mu.Lock()
	if png, ok := mermaidCache.byID[key]; ok {
		mermaidCache.mu.Unlock()
		return png, nil
	}
	mermaidCache.mu.Unlock()

	if !MermaidAvailable() {
		return nil, fmt.Errorf("%s is not installed", MermaidCommand)
	}

	dir, err := os.MkdirTemp("", "evilmermaid")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "diagram.mmd")
	out := filepath.Join(dir, "diagram.png")
	if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, MermaidTimeout)
	defer cancel()

	// A dark background because every built-in palette is dark; a diagram on
	// white in a dark terminal is a flashbang.
	cmd := exec.CommandContext(ctx, MermaidCommand,
		"-i", in, "-o", out, "-b", "transparent", "-t", "dark")
	cmd.Dir = dir
	// mmdc's browser holds pipes open past the process it starts, which is the
	// same hang that bit `bash` (LOOPS, exec timeout).
	cmd.WaitDelay = 2 * time.Second

	if combined, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s failed: %w\n%s", MermaidCommand, err,
			strings.TrimSpace(string(combined)))
	}

	png, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("%s wrote no image: %w", MermaidCommand, err)
	}

	mermaidCache.mu.Lock()
	if mermaidCache.byID == nil {
		mermaidCache.byID = map[string][]byte{}
	}
	mermaidCache.byID[key] = png
	mermaidCache.mu.Unlock()
	return png, nil
}

// CachedMermaid returns an already-rendered diagram, if there is one.
//
// The render loop uses this: rendering is slow and asynchronous, so a frame
// asks what is ready rather than waiting for what is not.
func CachedMermaid(src string) ([]byte, bool) {
	mermaidCache.mu.Lock()
	defer mermaidCache.mu.Unlock()
	png, ok := mermaidCache.byID[MermaidKey(src)]
	return png, ok
}
