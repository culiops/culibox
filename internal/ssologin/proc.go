package ssologin

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// proc is one running `aws sso login`. Kill ends its whole process group.
type proc interface {
	Output() io.Reader
	Wait() error
	Kill()
}

// outputStream is where the CLI prints the URL and the code, as measured by
// the M3b Task 1 spike (first line of testdata/device-code.txt). The other
// stream is discarded.
const outputStream = "stdout"

// drainGrace is how long after the process exits its output pipe stays open
// for the reader to drain, so a stray child holding the pipe cannot pin it.
const drainGrace = 500 * time.Millisecond

func realStart(bin string, args, env []string) (proc, error) {
	cmd := exec.Command(bin, args...) // no shell; argv is fixed (Args)
	cmd.Env = env                     // curated; never nil (nil would inherit the daemon's environment)
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Our own pipe, not StdoutPipe: cmd.Wait must not close the read side
	// before the reader has drained it.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if outputStream == "stderr" {
		cmd.Stderr = pw
	} else {
		cmd.Stdout = pw
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close() // the child holds the only write end now
	return &osProc{cmd: cmd, out: pr}, nil
}

type osProc struct {
	cmd    *exec.Cmd
	out    *os.File
	waited atomic.Bool
}

func (p *osProc) Output() io.Reader { return p.out }

func (p *osProc) Wait() error {
	err := p.cmd.Wait()
	p.waited.Store(true)
	time.AfterFunc(drainGrace, func() { p.out.Close() })
	return err
}

// Kill sends SIGKILL to the whole group. After Wait has reaped the leader it
// does nothing, so a recycled pid is never signalled.
func (p *osProc) Kill() {
	if p.waited.Load() {
		return
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = p.cmd.Process.Kill()
	}
}
