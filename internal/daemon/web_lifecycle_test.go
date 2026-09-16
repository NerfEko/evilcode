package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"evilcode/internal/config"
)

// newLifecycleServer builds a daemon server with an isolated config/data dir
// and a fresh socket path. It does not start Serve; the caller decides.
func newLifecycleServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	t.Setenv("EVILCODE_PROVIDER", "mock")
	t.Setenv("EVILCODE_SCENARIO", "chat")
	t.Setenv("EVILCODE_CONFIG", filepath.Join(home, "nonexistent.toml"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")

	srv := NewServer(cfg, t.TempDir(), "")
	srv.Path = path
	return srv
}

// EC-004: s.web must never be visible with a nil srv. The old code published
// w before building the mux/server, so a concurrent Close (which reads
// web.srv) raced the assignment. Spin readers during ListenWeb; they must
// never observe a half-initialized entry.
func TestWebPublishFullyInitialized_EC004(t *testing.T) {
	for i := 0; i < 50; i++ {
		srv := newLifecycleServer(t)
		start := make(chan struct{})
		done := make(chan struct{})
		var sawHalf int32
		var wg sync.WaitGroup
		// Readers mimic Close's access pattern: copy the pointer under s.mu,
		// then inspect srv outside the lock.
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for {
					select {
					case <-done:
						return
					default:
					}
					srv.mu.Lock()
					w := srv.web
					srv.mu.Unlock()
					if w != nil && (w.srv == nil || w.ln == nil) {
						atomic.StoreInt32(&sawHalf, 1)
						return
					}
				}
			}()
		}
		wg.Add(1)
		var listenErr error
		go func() {
			defer wg.Done()
			<-start
			listenErr = srv.ListenWeb("127.0.0.1:0")
		}()
		close(start)
		// Let the readers spin while ListenWeb binds, mints, and builds the
		// mux (the old window covered the whole mux build).
		time.Sleep(20 * time.Millisecond)
		close(done)
		wg.Wait()
		if atomic.LoadInt32(&sawHalf) != 0 {
			t.Fatalf("iter %d: observed s.web with nil srv/ln (half-published)", i)
		}
		if listenErr != nil {
			t.Fatalf("iter %d: ListenWeb: %v", i, listenErr)
		}
		srv.mu.Lock()
		w := srv.web
		srv.mu.Unlock()
		if w == nil || w.srv == nil || w.ln == nil {
			t.Fatalf("iter %d: published web state incomplete: %+v", i, w)
		}
		srv.Close()
	}
}

// EC-010: bind first, then mint. A refused bind must leave no token file
// behind and must not disturb a pre-existing one.
func TestWebBindFailureLeavesNoToken_EC010(t *testing.T) {
	srv := newLifecycleServer(t)
	tokenPath := srv.webTokenPath()
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file pre-exists: %v", err)
	}

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	occupied := blocker.Addr().String()

	if err := srv.ListenWeb(occupied); err == nil {
		blocker.Close()
		t.Fatal("ListenWeb on an occupied port must fail")
	}
	blocker.Close()

	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed bind created token file: stat err %v", err)
	}
	if srv.WebInfo() != nil {
		t.Fatal("WebInfo non-nil after failed bind")
	}
	if got := srv.webAddr(); got != "" {
		t.Fatalf("webAddr after failed bind = %q, want empty", got)
	}

	// Success path still mints once.
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatalf("ListenWeb after failed bind: %v", err)
	}
	defer srv.Close()
	info := srv.WebInfo()
	if info == nil {
		t.Fatal("WebInfo nil after successful bind")
	}
	if !info.Minted {
		t.Error("first successful bind should report Minted=true")
	}
	if info.Token == "" {
		t.Error("WebInfo token empty after mint")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("reading minted token: %v", err)
	}
	if strings.TrimSpace(string(data)) != info.Token {
		t.Error("token file does not match WebInfo token")
	}

	// A later bind failure must preserve the existing token file.
	before, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	blocker2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker2.Close()
	_ = srv.ListenWeb(blocker2.Addr().String())
	after, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("existing token file missing after failed re-bind: %v", err)
	}
	if string(before) != string(after) {
		t.Error("failed bind modified the pre-existing token file")
	}
	// The live listener survives the failed re-bind.
	if srv.WebInfo() == nil || srv.WebInfo().Addr != info.Addr {
		t.Error("live web listener disturbed by failed re-bind")
	}
}

// EC-011: when Serve exits unexpectedly, the stale s.web entry must be
// cleared (same pointer only) so a new listener can start.
func TestWebServeExitClearsStale_EC011(t *testing.T) {
	srv := newLifecycleServer(t)
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatalf("ListenWeb: %v", err)
	}
	srv.mu.Lock()
	w := srv.web
	srv.mu.Unlock()
	if w == nil {
		t.Fatal("no web state after ListenWeb")
	}
	oldAddr := w.addr

	// Kill the listener underneath Serve without going through Close.
	w.ln.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if srv.WebInfo() == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale s.web not cleared after Serve exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.webAddr(); got != "" {
		t.Fatalf("webAddr after Serve exit = %q, want empty", got)
	}

	// A successor listener must start on a fresh port.
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatalf("ListenWeb after Serve exit: %v", err)
	}
	defer srv.Close()
	info := srv.WebInfo()
	if info == nil {
		t.Fatal("WebInfo nil after re-listen")
	}
	if info.Addr == oldAddr {
		t.Logf("note: re-listen reused addr %s (unlikely but legal)", oldAddr)
	}
}

// EC-012: Serve must not leak a ctx watcher when Close is called with a
// non-canceling context. AfterFunc + defer stop() unregisters; the old
// `go <-ctx.Done()` blocked forever on Background.
func TestServeBackgroundCloseNoLeak_EC012(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	t.Setenv("EVILCODE_PROVIDER", "mock")
	t.Setenv("EVILCODE_SCENARIO", "chat")
	t.Setenv("EVILCODE_CONFIG", filepath.Join(home, "nonexistent.toml"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	// Baseline after letting the test binary settle.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		dir, err := os.MkdirTemp("", "evild")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "s.sock")
		srv := NewServer(cfg, t.TempDir(), "")
		srv.Path = path
		if err := srv.Listen(); err != nil {
			os.RemoveAll(dir)
			t.Fatalf("iter %d: Listen: %v", i, err)
		}
		served := make(chan error, 1)
		go func() { served <- srv.Serve(context.Background()) }()
		// Give Serve a moment to block in Accept, then close explicitly.
		time.Sleep(10 * time.Millisecond)
		srv.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Fatalf("iter %d: Serve with Background returned %v, want nil", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: Serve with Background did not return after Close", i)
		}
		os.RemoveAll(dir)
	}

	// Let reaped goroutines exit, then check growth. A leaked watcher per
	// iteration would add ~20 blocked goroutines here.
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.Gosched()
		time.Sleep(20 * time.Millisecond)
		if runtime.NumGoroutine() <= baseline+5 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if got := runtime.NumGoroutine(); got > baseline+10 {
		t.Fatalf("goroutines grew from %d to %d after 20 Background Serve+Close cycles (watcher leak?)", baseline, got)
	}
}
