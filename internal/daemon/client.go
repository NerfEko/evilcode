package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client is a connection to a running daemon.
type Client struct {
	conn net.Conn
	sc   *bufio.Scanner

	mu  sync.Mutex
	enc *json.Encoder
}

// Dial connects to the daemon's socket.
func Dial() (*Client, error) {
	return DialPath(SocketPath())
}

// DialPath connects to a specific socket, which is what a test wants.
func DialPath(path string) (*Client, error) {
	if err := CheckSocketPath(path); err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("no daemon at %s (start one with `evilcode serve`): %w", path, err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &Client{conn: conn, sc: sc, enc: json.NewEncoder(conn)}, nil
}

// Close hangs up.
func (c *Client) Close() error { return c.conn.Close() }

// Send writes one frame. It is safe to call from any goroutine.
func (c *Client) Send(msg ClientMsg) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(msg)
}

// Recv reads the next frame. It returns io.EOF when the daemon hangs up.
func (c *Client) Recv() (ServerMsg, error) {
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return ServerMsg{}, err
		}
		return ServerMsg{}, ErrClosed
	}
	var msg ServerMsg
	if err := json.Unmarshal(c.sc.Bytes(), &msg); err != nil {
		return ServerMsg{}, err
	}
	return msg, nil
}

// ErrClosed is returned by Recv when the daemon closed the connection.
var ErrClosed = fmt.Errorf("daemon closed the connection")

// List asks the daemon what it is holding.
func (c *Client) List() ([]SessionInfo, error) {
	if err := c.Send(ClientMsg{Kind: MsgList}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			return nil, err
		}
		switch msg.Kind {
		case MsgSessions:
			return msg.Sessions, nil
		case MsgError:
			return nil, fmt.Errorf("%s", msg.Err)
		}
		// Anything else is a broadcast for a session this connection is
		// attached to; keep reading rather than treating it as an answer.
	}
}

// Attach subscribes to a session and returns its snapshot. An empty name
// creates a new session.
func (c *Client) Attach(name string, since int) (*Snapshot, error) {
	if err := c.Send(ClientMsg{Kind: MsgAttach, Session: name, Since: since}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			return nil, err
		}
		switch msg.Kind {
		case MsgSnapshot:
			return msg.Snapshot, nil
		case MsgError:
			return nil, fmt.Errorf("%s", msg.Err)
		}
	}
}
