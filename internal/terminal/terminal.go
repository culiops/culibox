// Package terminal streams a tmux session's PTY over a WebSocket to an
// xterm.js frontend, in-process (no ttyd, no iframe).
//
// Wire protocol (browser <-> server):
//   - binary frame  = raw terminal bytes (input in, output out)
//   - text frame    = JSON control, currently {"type":"resize","rows":R,"cols":C}
//
// Concurrency invariant: exactly one goroutine writes to the socket (pty->ws);
// the request goroutine only reads the socket and writes to the PTY. This
// satisfies coder/websocket's one-reader/one-writer rule.
package terminal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// ResizeFunc resizes the underlying PTY.
type ResizeFunc func(rows, cols uint16) error

// PTYFactory opens a PTY for a named session. Injectable for testing.
type PTYFactory func(name string) (io.ReadWriteCloser, ResizeFunc, error)

type control struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// WSHandler upgrades a request to a WebSocket and bridges it to a PTY.
type WSHandler struct {
	factory        PTYFactory
	originPatterns []string
}

// NewWSHandler builds a handler. With no originPatterns, coder/websocket's
// default same-origin check applies; the WS route is additionally gated by an
// auth cookie at the mux layer.
func NewWSHandler(f PTYFactory, originPatterns ...string) *WSHandler {
	return &WSHandler{factory: f, originPatterns: originPatterns}
}

func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ws/")
	if name == "" || strings.Contains(name, "/") {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}
	opts := &websocket.AcceptOptions{}
	if len(h.originPatterns) > 0 {
		opts.OriginPatterns = h.originPatterns
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return // Accept already wrote the response
	}
	defer c.CloseNow()

	ptyRW, resize, err := h.factory(name)
	if err != nil {
		c.Close(websocket.StatusInternalError, "pty start failed")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	done := make(chan struct{})
	// pty -> ws: the sole socket writer.
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, rerr := ptyRW.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				cancel()
				return
			}
		}
	}()

	// ws -> pty: read loop (writes only to the PTY).
loop:
	for {
		typ, data, rerr := c.Read(ctx)
		if rerr != nil {
			break
		}
		switch typ {
		case websocket.MessageBinary:
			if _, werr := ptyRW.Write(data); werr != nil {
				break loop
			}
		case websocket.MessageText:
			var ctl control
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				_ = resize(ctl.Rows, ctl.Cols)
			}
		}
	}
	// Closing the PTY unblocks the writer goroutine (and reaps the child).
	_ = ptyRW.Close()
	cancel()
	<-done
}
