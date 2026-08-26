package daemon

import (
	"os"
	"testing"
	"time"
)

// D6: a stop acknowledgement must mean shutdown completed. The old server
// replied with its status and closed asynchronously, so the updater could
// rename the executable while the daemon was still running tools, writing
// state, or owning the socket. Stop must return only after teardown: the
// socket file is gone by the time it returns.
func TestStopReturnsOnlyAfterShutdownCompletes(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	// Open and attach a real session so teardown has something to close.
	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Attach("", 0); err != nil {
		t.Fatal(err)
	}
	client.Close()

	// A second client requests the stop and must not return until teardown
	// has unlinked the socket.
	stopper, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = stopper.SetDeadline(15 * time.Second)
	start := time.Now()
	if err := stopper.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	stopper.Close()
	elapsed := time.Since(start)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket %s still exists after Stop returned (elapsed %s); teardown must precede the acknowledgement", path, elapsed)
	}
}
