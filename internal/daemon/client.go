package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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

// SetDeadline bounds every Send and Recv from now on. A zero d clears it.
//
// A daemon that accepts the connection and then stalls — wedged, overloaded,
// whatever the reason — otherwise leaves Recv blocked forever, and for a
// caller on the UI's update loop that means the whole interface freezes with
// no way to type past it.
func (c *Client) SetDeadline(d time.Duration) error {
	var t time.Time
	if d > 0 {
		t = time.Now().Add(d)
	}
	return c.conn.SetDeadline(t)
}

// Send writes one frame. It is safe to call from any goroutine.
//
// The frame is encoded into memory first so an oversized payload is refused
// with a typed error instead of overflowing the server's scanner, which would
// terminate the connection before the daemon ever parsed the prompt (D1).
func (c *Client) Send(msg ClientMsg) error {
	if msg.Version == 0 {
		msg.Version = ProtocolVersion
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(msg); err != nil {
		return err
	}
	if buf.Len() > MaxClientFrameBytes {
		return fmt.Errorf("%w: %d bytes (images inflate by ~1/3 in base64; attach fewer or smaller images)",
			ErrFrameTooLarge, buf.Len())
	}
	return c.enc.Encode(msg)
}

// Recv reads the next frame. It returns io.EOF when the daemon hangs up.
func (c *Client) Recv() (ServerMsg, error) {
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				return ServerMsg{}, fmt.Errorf("%w (server→client)", ErrFrameTooLarge)
			}
			return ServerMsg{}, err
		}
		return ServerMsg{}, ErrClosed
	}
	var msg ServerMsg
	if err := json.Unmarshal(c.sc.Bytes(), &msg); err != nil {
		return ServerMsg{}, err
	}
	if msg.Version != 0 && msg.Version != ProtocolVersion {
		return ServerMsg{}, fmt.Errorf("unsupported daemon protocol version %d (want %d)", msg.Version, ProtocolVersion)
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
	return c.AttachAt(name, since, "", "")
}

// AttachAt attaches to a session, supplying the client's workspace/model when
// the session does not exist yet.
func (c *Client) AttachAt(name string, since int, cwd, model string) (*Snapshot, error) {
	return c.AttachWithOptions(name, since, cwd, model, false)
}

// AttachWithOptions attaches while preserving headless options for a new
// session, such as -no-tools.
func (c *Client) AttachWithOptions(name string, since int, cwd, model string, noTools bool) (*Snapshot, error) {
	if err := c.Send(ClientMsg{
		Kind: MsgAttach, Session: name, Since: since, Cwd: cwd, Model: model, NoTools: noTools,
	}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			return nil, err
		}
		switch msg.Kind {
		case MsgSnapshot:
			if msg.Snapshot == nil {
				return nil, fmt.Errorf("daemon sent an empty session snapshot")
			}
			return msg.Snapshot, nil
		case MsgError:
			return nil, fmt.Errorf("%s", msg.Err)
		}
	}
}

// Status asks the daemon for lifecycle information.
func (c *Client) Status() (*ServerStatus, error) {
	if err := c.Send(ClientMsg{Kind: MsgStatus}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			return nil, err
		}
		switch msg.Kind {
		case MsgStatus:
			if msg.Status == nil {
				return nil, fmt.Errorf("daemon sent an empty status")
			}
			return msg.Status, nil
		case MsgError:
			return nil, fmt.Errorf("%s", msg.Err)
		}
	}
}

// Stop requests a graceful daemon shutdown.
//
// "Stopped" means shutdown completed: the daemon closes this connection only
// after teardown (socket unlinked, sessions closed), so the status reply is
// progress, not completion. Returning on the status would let the updater
// rename the executable while the old daemon was still running tools or
// writing state (D6). Callers bound the wait with SetDeadline.
func (c *Client) Stop() error {
	if err := c.Send(ClientMsg{Kind: MsgStop}); err != nil {
		return err
	}
	for {
		msg, err := c.Recv()
		if err != nil {
			// The daemon closes the connection after teardown; EOF is the
			// completion acknowledgement.
			if errors.Is(err, ErrClosed) {
				return nil
			}
			return err
		}
		if msg.Kind == MsgError {
			return fmt.Errorf("%s", msg.Err)
		}
	}
}
