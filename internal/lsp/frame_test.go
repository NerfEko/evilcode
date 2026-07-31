package lsp

import (
	"bufio"
	"runtime"
	"strings"
	"testing"
)

// H3.10: the protocol reader trusted Content-Length and allocated the whole
// body up front. A server that is broken — or a proxy that mangled a frame —
// can name a size larger than memory, and the allocation happens before a
// single byte of the body has arrived.
func TestAnOversizedFrameIsRefusedNotAllocated(t *testing.T) {
	// 64 GiB announced, nothing behind it.
	header := "Content-Length: 68719476736\r\n\r\n"
	c := &Client{out: bufio.NewReader(strings.NewReader(header))}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := c.readMessage()
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("a 64 GiB frame was accepted")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want it to name the size limit", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("refusing the frame still allocated %d bytes", grew)
	}
}

func TestANegativeFrameIsRefused(t *testing.T) {
	c := &Client{out: bufio.NewReader(strings.NewReader("Content-Length: -1\r\n\r\n"))}
	if _, err := c.readMessage(); err == nil {
		t.Error("a negative Content-Length was accepted")
	}
}

// A frame within the limit still round-trips.
func TestAnOrdinaryFrameIsRead(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{}}`
	frame := "Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body
	c := &Client{out: bufio.NewReader(strings.NewReader(frame))}

	got, err := c.readMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("read %q", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
