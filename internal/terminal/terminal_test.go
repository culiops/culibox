package terminal

import (
	"context"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestWSHandler_PipesBytesBothWays uses a net.Pipe as the PTY stand-in and
// sends input the way the browser will (binary frame), so the test exercises
// the real wire protocol rather than a tautology.
func TestWSHandler_PipesBytesBothWays(t *testing.T) {
	resized := make(chan [2]uint16, 1)
	ptySide, appSide := net.Pipe()
	f := func(name string) (io.ReadWriteCloser, ResizeFunc, error) {
		return appSide, func(r, c uint16) error { resized <- [2]uint16{r, c}; return nil }, nil
	}
	ts := httptest.NewServer(NewWSHandler(f))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/demo"
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()

	// client -> PTY (browser sends keystrokes as binary)
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	readDone := make(chan error, 1)
	go func() { _, e := io.ReadFull(ptySide, buf); readDone <- e }()
	select {
	case e := <-readDone:
		if e != nil {
			t.Fatalf("read pty: %v", e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timeout waiting for bytes at pty")
	}
	if string(buf) != "hello" {
		t.Fatalf("pty got %q", buf)
	}

	// PTY -> client
	go func() { ptySide.Write([]byte("world")) }()
	typ, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageBinary || string(data) != "world" {
		t.Fatalf("client got typ=%v data=%q", typ, data)
	}

	// resize control frame (text)
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":40,"cols":120}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case rc := <-resized:
		if rc != [2]uint16{40, 120} {
			t.Fatalf("resize got %v", rc)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timeout waiting for resize")
	}
}
