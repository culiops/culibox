package terminal

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/session"
)

type catBuilder struct{}

func (catBuilder) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return []string{"cat"}, nil, nil
}

// TestAttachHandler drives the real handler against a real `cat` session:
// input sent as binary (the way the browser does) echoes back as binary output,
// and a text resize frame is accepted. Unknown ids 404.
func TestAttachHandler(t *testing.T) {
	m := session.NewManager(catBuilder{}, nil, 1<<16, time.Minute)
	sess, err := m.Spawn(launcher.SpawnSpec{Agent: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(sess.ID)

	ts := httptest.NewServer(NewAttachHandler(m.Get))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// unknown id → the WS dial fails on the 404
	if _, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/nope"), nil); err == nil {
		t.Fatal("dial to unknown session should fail (404)")
	}

	ws, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/"+sess.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()

	// resize control frame first (text)
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":30,"cols":100}`)); err != nil {
		t.Fatal(err)
	}
	// input as binary → cat echoes it back
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("ping\n")); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("did not see echoed input; got %q (err %v)", b.String(), err)
		}
		if typ == websocket.MessageBinary {
			b.Write(data)
			if strings.Contains(b.String(), "ping") {
				return
			}
		}
	}
}

// A second attach evicts the first, and the evicted socket must be TOLD so —
// with the private-range close code, not an abrupt drop. Without the code the
// client cannot tell a takeover from a dead server, reconnects, evicts the other
// viewer, and the two ping-pong forever.
func TestAttachHandler_SupersededCloseCode(t *testing.T) {
	m := session.NewManager(catBuilder{}, nil, 1<<16, time.Minute)
	sess, err := m.Spawn(launcher.SpawnSpec{Agent: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(sess.ID)

	ts := httptest.NewServer(NewAttachHandler(m.Get))
	defer ts.Close()

	// Not a window the eviction must land in: every step below waits for an event, and
	// this only keeps a real hang from stalling the run (a loaded runner is slow, not wrong).
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	first, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/"+sess.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()

	// Dial returns once the 101 is written, before the handler has called Subscribe. A
	// second dial straight after can have its handler subscribe FIRST, which makes the
	// first handler the one that supersedes it: then it is the second socket that gets
	// 4001, and the first one, the survivor, is read until the deadline (#311, measured:
	// 13 of 600 runs with -cpu 1,2 on a busy host). Output reaches a socket only through
	// its subscription, so an echo seen on the first one proves it is the subscribed
	// viewer, and the second attach is then certain to evict it.
	if err := first.Write(ctx, websocket.MessageBinary, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := readUntil(ctx, first, "first"); err != nil {
		t.Fatalf("first viewer never saw its echo; got %q (err %v)", got, err)
	}

	second, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/"+sess.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.CloseNow()

	// Read until the first socket dies; it must die with the superseded code.
	for {
		_, _, err := first.Read(ctx)
		if err == nil {
			continue // replayed ring output — keep reading
		}
		if got := websocket.CloseStatus(err); got != wsSuperseded {
			t.Fatalf("evicted socket closed with status %d, want %d (err %v)", got, wsSuperseded, err)
		}
		break
	}

	// Evicting the first viewer must leave the second one working — the whole
	// point is that the session moves, not that it breaks.
	if err := second.Write(ctx, websocket.MessageBinary, []byte("pong\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := readUntil(ctx, second, "pong"); err != nil {
		t.Fatalf("survivor stopped working after the eviction; got %q (err %v)", got, err)
	}
}

// readUntil reads binary output from c until it contains want; it returns what it read.
func readUntil(ctx context.Context, c *websocket.Conn, want string) (string, error) {
	var b strings.Builder
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return b.String(), err
		}
		if typ == websocket.MessageBinary {
			b.Write(data)
			if strings.Contains(b.String(), want) {
				return b.String(), nil
			}
		}
	}
}

// A socket that is NOT evicted must keep today's behaviour when the session ends:
// no superseded code, so the client's reconnect/ended path stays untouched.
func TestAttachHandler_SessionEndIsNotSuperseded(t *testing.T) {
	m := session.NewManager(catBuilder{}, nil, 1<<16, time.Minute)
	sess, err := m.Spawn(launcher.SpawnSpec{Agent: "cat"})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(NewAttachHandler(m.Get))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, wsURL(ts.URL, "/ws/"+sess.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()

	if err := m.Kill(sess.ID); err != nil {
		t.Fatal(err)
	}
	for {
		_, _, err := ws.Read(ctx)
		if err == nil {
			continue
		}
		if got := websocket.CloseStatus(err); got == wsSuperseded {
			t.Fatalf("a session that ended reported itself superseded (err %v)", err)
		}
		return
	}
}

func wsURL(httpURL, path string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + path
}
