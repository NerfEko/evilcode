package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Peer is one other agent in the swarm, as the coordination tools describe it.
type Peer struct {
	Name    string
	Task    string
	Worker  bool
	Running bool
	// Stale means a worker remains registered but has not advanced its event
	// heartbeat within the daemon's liveness window.
	Stale bool
	Files []string
	Since time.Duration
}

// Messenger is what the messaging tools need from the daemon. Separate from
// Swarm because a session can be reachable without being allowed to spawn.
type Messenger interface {
	// Self is the calling session's name.
	Self() string

	// SendMessage delivers to one named agent.
	SendMessage(to, text string) error

	// Broadcast delivers to every other agent and reports how many heard it.
	Broadcast(text string) int

	// Peers lists the other agents.
	Peers() []Peer
}

// NewMessaging returns send_message, broadcast, and peers (plan.md §20).
//
// They are registered only for a session inside the daemon: outside one there
// is nobody to talk to, and a tool that is present and always fails is worse
// than one that is absent.
func NewMessaging(m Messenger) Set {
	return Set{sendMessageTool(m), broadcastTool(m), peersTool(m)}
}

type sendArgs struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

func sendMessageTool(m Messenger) Tool {
	return Tool{
		Name: "send_message",
		Desc: "Send a message to another agent in this swarm. It reaches them between " +
			"turns rather than mid-thought. Use `peers` to see who is here.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "to":   {"type": "string", "description": "The agent's session name"},
    "text": {"type": "string", "description": "What to tell them"}
  },
  "required": ["to", "text"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a sendArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.To) == "" || strings.TrimSpace(a.Text) == "" {
				return Result{}, fmt.Errorf("to and text are both required")
			}
			if a.To == m.Self() {
				return Result{}, fmt.Errorf("%q is you; send to another agent", a.To)
			}
			if err := m.SendMessage(a.To, a.Text); err != nil {
				return Result{}, err
			}
			return Result{Output: "delivered to " + a.To, Intent: "→ " + a.To}, nil
		},
	}
}

type broadcastArgs struct {
	Text string `json:"text"`
}

func broadcastTool(m Messenger) Tool {
	return Tool{
		Name: "broadcast",
		Desc: "Tell every other agent in the swarm something. Use it for facts that " +
			"change what they should do — a file you are about to rewrite, a " +
			"decision that invalidates what they assumed.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "What everyone needs to know"}
  },
  "required": ["text"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a broadcastArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.Text) == "" {
				return Result{}, fmt.Errorf("text is required")
			}
			n := m.Broadcast(a.Text)
			if n == 0 {
				return Result{Output: "nobody else is here; nothing was sent"}, nil
			}
			return Result{
				Output: fmt.Sprintf("sent to %d %s", n, agentNoun(n)),
				Intent: fmt.Sprintf("📣 %d", n),
			}, nil
		},
	}
}

func peersTool(m Messenger) Tool {
	return Tool{
		Name: "peers",
		Desc: "List the other agents in this swarm, what each is working on, and " +
			"which files they have touched.",
		Schema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			peers := m.Peers()
			if len(peers) == 0 {
				return Result{Output: "you are the only agent here"}, nil
			}
			sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })

			var b strings.Builder
			for _, p := range peers {
				state := "idle"
				if p.Stale {
					state = "stale"
				} else if p.Running {
					state = "working"
				}
				fmt.Fprintf(&b, "%s (%s)", p.Name, state)
				if p.Task != "" {
					fmt.Fprintf(&b, " — %s", shortTask(p.Task))
				}
				b.WriteString("\n")
				if len(p.Files) > 0 {
					shown, suffix := p.Files, ""
					if len(shown) > 5 {
						suffix = fmt.Sprintf(" (+%d more)", len(shown)-5)
						shown = shown[:5]
					}
					fmt.Fprintf(&b, "  files: %s%s\n", strings.Join(shown, ", "), suffix)
				}
			}
			return Result{
				Output: strings.TrimRight(b.String(), "\n"),
				Intent: fmt.Sprintf("%d %s", len(peers), agentNoun(len(peers))),
			}, nil
		},
	}
}

func agentNoun(n int) string {
	if n == 1 {
		return "agent"
	}
	return "agents"
}
