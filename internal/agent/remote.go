package agent

import "context"

// The remote seam (plan.md §20).
//
// `evilcode attach` runs the same TUI against a session living in the daemon.
// Rather than teaching the TUI to talk to two different things, the client
// builds an ordinary Agent whose turns are forwarded and whose event stream is
// fed from the socket. The TUI cannot tell the difference, which is the point
// of invariant 1: one frontend, one event stream, and the network is a detail
// underneath both.

// Remote diverts a turn instead of running the loop. When Agent.Forward is set,
// the agent never calls a provider — Run hands the input to it and returns.
//
// It lives on the Agent rather than being a separate type because everything
// else the TUI reads — Running, the conversation, pending interrupts — must
// keep working, and a parallel implementation of all of that would drift.
type Remote func(ctx context.Context, userInput string) error

// RemoteImages is the image-aware form of Remote.
type RemoteImages func(ctx context.Context, userInput string, images [][]byte) error

// Inject pushes an event into the stream as though the loop had produced it.
//
// This is how a remote session's events reach the frontend. It is deliberately
// the only way in from outside: the loop's own events go through emit, so there
// is exactly one channel and no second ordering to reason about.
func (a *Agent) Inject(e Event) {
	if e.Session == "" {
		e.Session = a.Session
	}
	a.emit(e)
}

// SetRunning marks a turn in flight. An attached client is told whether the
// remote turn is running; without this the composer would show an idle prompt
// while the far side is streaming.
func (a *Agent) SetRunning(running bool) {
	a.mu.Lock()
	a.running = running
	a.mu.Unlock()
}
