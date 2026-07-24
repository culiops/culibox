// Package terminal bridges a WebSocket to a daemon-owned session's PTY stream.
// It is a pure transport adapter: session output → binary frames; binary frames
// → session input; text frames → resize control. No PTY or process logic lives
// here (that is the session package).
package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/culiops/culiops-agent-console/internal/session"
)

type control struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// AttachHandler serves /ws/{id}, attaching the socket to a live session.
type AttachHandler struct {
	get            func(id string) (*session.Session, bool)
	originPatterns []string
}

// NewAttachHandler builds the handler. With no originPatterns, coder/websocket's
// default same-origin check applies; the route is additionally cookie-gated at
// the mux layer.
func NewAttachHandler(get func(id string) (*session.Session, bool), originPatterns ...string) *AttachHandler {
	return &AttachHandler{get: get, originPatterns: originPatterns}
}

func (h *AttachHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/ws/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	sess, ok := h.get(id)
	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}

	opts := &websocket.AcceptOptions{}
	if len(h.originPatterns) > 0 {
		opts.OriginPatterns = h.originPatterns
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer c.CloseNow()

	out, cancel := sess.Subscribe()
	defer cancel()

	ctx, cancelCtx := context.WithCancel(r.Context())
	defer cancelCtx()

	// output: session -> ws. Sole writer to the socket (Write AND Ping live here so
	// there is never a second concurrent writer). `out` closes when the session ends
	// or this subscription is cancelled. The input Read loop below is the concurrent
	// reader that Ping needs to receive its pong.
	go func() {
		defer cancelCtx()
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case b, ok := <-out:
				if !ok {
					return
				}
				// Bounded write so a black-hole client can't hang this goroutine.
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := c.Write(wctx, websocket.MessageBinary, b)
				wcancel()
				if err != nil {
					return
				}
			case <-ping.C:
				// App-level keepalive: detect a dead/half-open peer (esp. an idle
				// session with no output, which TCP keepalive is too slow to catch)
				// so the socket closes and the client reconnects.
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := c.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// input: ws -> session (only writes to the PTY, never the socket).
	firstResize := true
loop:
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := sess.Write(data); err != nil {
				break loop
			}
		case websocket.MessageText:
			var ctl control
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				_ = sess.Resize(ctl.Rows, ctl.Cols)
				if firstResize {
					firstResize = false
					sess.ForceRedraw() // repaint a TUI over the best-effort replay
				}
			}
		}
	}
	cancelCtx()
}
