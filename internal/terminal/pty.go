package terminal

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/culiops/culiops-agent-console/internal/tmux"
)

// ptySession is an io.ReadWriteCloser over a PTY whose Close also reaps the
// child process (no zombie `tmux attach` per opened-then-closed pane).
type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *ptySession) Close() error {
	// Closing the PTY master sends SIGHUP to `tmux attach`, which detaches from
	// the server cleanly and exits. Prefer that over an immediate SIGKILL:
	// killing the attach client abruptly can leave a half-closed client fd on
	// the server that wedges its event loop into a busy-poll spin, after which
	// the server refuses all new client connections ("server exited
	// unexpectedly"). Escalate to SIGTERM, then SIGKILL, only if it hangs.
	err := p.f.Close()
	if p.cmd.Process == nil {
		return err
	}
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
	}
	return err
}

// TmuxAttachFactory attaches to an existing tmux session via a PTY. It sets
// TERM (else `tmux attach` fails) and an initial 80x24 size (else tmux sizes
// to a degenerate client until the first resize frame).
func TmuxAttachFactory(name string) (io.ReadWriteCloser, ResizeFunc, error) {
	cmd := exec.Command("tmux", "-L", tmux.SocketName, "attach", "-d", "-t", name)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, nil, err
	}
	resize := func(rows, cols uint16) error {
		return pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols})
	}
	return &ptySession{f: f, cmd: cmd}, resize, nil
}
