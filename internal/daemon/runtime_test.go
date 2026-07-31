package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// H4.4: the /tmp fallback runtime directory is used whenever XDG_RUNTIME_DIR is
// unset. MkdirAll does nothing to a directory that already exists, so an
// attacker who created it first owns the path the socket is bound in — and that
// socket carries a live shell.
func TestASquattedRuntimeDirectoryIsRefused(t *testing.T) {
	base := t.TempDir()

	for _, tc := range []struct {
		name  string
		setup func(dir string) error
	}{
		{"world-writable", func(dir string) error {
			if err := os.MkdirAll(dir, 0o777); err != nil {
				return err
			}
			return os.Chmod(dir, 0o777)
		}},
		{"group-writable", func(dir string) error {
			if err := os.MkdirAll(dir, 0o770); err != nil {
				return err
			}
			return os.Chmod(dir, 0o770)
		}},
		{"a symlink somewhere else", func(dir string) error {
			target := filepath.Join(base, "elsewhere")
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Symlink(target, dir)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(base, strings.ReplaceAll(tc.name, " ", "-"))
			if err := tc.setup(dir); err != nil {
				t.Fatal(err)
			}
			if err := CheckRuntimeDir(dir); err == nil {
				t.Errorf("a %s runtime directory was accepted; anything that can "+
					"reach the socket in it can run commands as this user", tc.name)
			}
		})
	}
}

func TestAnOwnedPrivateRuntimeDirectoryIsAccepted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckRuntimeDir(dir); err != nil {
		t.Errorf("a private, owned directory was refused: %v", err)
	}
}

// H4.5: two daemons starting together can both fail the liveness dial, and the
// second's Remove unlinks the first's freshly bound socket — leaving daemon one
// running and unreachable.
func TestASecondDaemonDoesNotUnlinkTheFirstsSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")

	first := NewServer(nil, t.TempDir(), "")
	first.Path = path
	if err := first.Listen(); err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second := NewServer(nil, t.TempDir(), "")
	second.Path = path
	if err := second.Listen(); err == nil {
		second.Close()
		t.Fatal("a second daemon bound the same socket")
	}

	// The first daemon must still be reachable.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the first daemon's socket was unlinked by the second: %v", err)
	}
	conn.Close()
}

// Two daemons finding the same *stale* socket both fail the liveness dial. The
// bind-first ordering fixed the original race but not this one: the second's
// removal could unlink the socket the first had by then bound.
func TestConcurrentStartsOnAStaleSocketLeaveOneReachable(t *testing.T) {
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")

	// A stale socket: a real one, bound and then abandoned without cleanup.
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if uc, ok := stale.(*net.UnixListener); ok {
		uc.SetUnlinkOnClose(false)
	}
	stale.Close()

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bound []*Server
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv := NewServer(nil, t.TempDir(), "")
			srv.Path = path
			if err := srv.Listen(); err != nil {
				return
			}
			mu.Lock()
			bound = append(bound, srv)
			mu.Unlock()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	defer func() {
		for _, srv := range bound {
			srv.Close()
		}
	}()

	if len(bound) != 1 {
		t.Fatalf("%d of %d daemons bound the same socket", len(bound), racers)
	}
	// And the one that won is the one that is reachable.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the daemon that bound the socket is unreachable: %v", err)
	}
	conn.Close()
}
