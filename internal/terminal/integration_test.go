//go:build integration

package terminal

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/culiops/culiops-agent-console/internal/tmux"
)

// TestTmuxAttach_Smoke really spawns tmux + a PTY and asserts the round trip.
// Run with: go test -tags integration ./internal/terminal/
// Setup runs on the console's dedicated socket (tmux -L SocketName) so it
// matches what TmuxAttachFactory attaches to.
func TestTmuxAttach_Smoke(t *testing.T) {
	_ = exec.Command("tmux", "-L", tmux.SocketName, "kill-session", "-t", "ac_smoke").Run()
	if err := exec.Command("tmux", "-L", tmux.SocketName, "new-session", "-d", "-s", "ac_smoke", "cat").Run(); err != nil {
		t.Fatalf("tmux new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", tmux.SocketName, "kill-session", "-t", "ac_smoke").Run()

	ts := httptest.NewServer(NewWSHandler(TmuxAttachFactory))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/ac_smoke"
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()

	time.Sleep(300 * time.Millisecond) // let the attach settle
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("ping\n")); err != nil {
		t.Fatal(err)
	}

	var got strings.Builder
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("did not see echoed input; got %q (err %v)", got.String(), err)
		}
		if typ == websocket.MessageBinary {
			got.Write(data)
			if strings.Contains(got.String(), "ping") {
				return // success: input reached the tmux PTY and echoed back
			}
		}
	}
}
