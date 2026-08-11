package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type peerTestMessenger struct {
	peers []Peer
}

func (m peerTestMessenger) Self() string                     { return "self" }
func (m peerTestMessenger) SendMessage(string, string) error { return nil }
func (m peerTestMessenger) Broadcast(string) int             { return 0 }
func (m peerTestMessenger) Peers() []Peer                    { return m.peers }

func TestPeersRendersAStaleWorkerDistinctly(t *testing.T) {
	tool := peersTool(peerTestMessenger{peers: []Peer{{
		Name: "worker", Running: true, Stale: true,
	}}})
	res, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "worker (stale)") {
		t.Errorf("peers output = %q, want stale worker state", res.Output)
	}
}
