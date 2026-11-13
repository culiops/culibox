//go:build upgrade

package upgradetest

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// tempPrefix is where every throwaway HOME lives: short, so abduco socket paths
// under the data root stay inside the 104/108-byte sun_path limit, and never the
// real user's home.
const tempPrefix = "/tmp/abup"

// Env is the whole environment of one serve process. RootEnv, when set, pins
// the data root through AGENT_BOX_HOME (both vN-1 and vN honour it).
type Env struct {
	Home    string
	RootEnv string
}

// Vars is the explicit environment: nothing is inherited except PATH (abduco)
// and USER, so a CULIBOX_HOME or CLAUDE_* in the caller's shell cannot leak in.
// It panics on a HOME outside tempPrefix: the test must never run a binary on
// the real user's data.
func (e Env) Vars() []string {
	if !strings.HasPrefix(e.Home, tempPrefix) {
		panic(fmt.Sprintf("upgradetest: HOME %q is not under %s", e.Home, tempPrefix))
	}
	u := os.Getenv("USER")
	if u == "" {
		if cu, err := user.Current(); err == nil {
			u = cu.Username
		}
	}
	v := []string{
		"HOME=" + e.Home,
		"PATH=" + os.Getenv("PATH"),
		"USER=" + u,
		"LANG=C.UTF-8",
		"SHELL=/bin/sh",
		"TMPDIR=/tmp",
	}
	if e.RootEnv != "" {
		v = append(v, "AGENT_BOX_HOME="+e.RootEnv)
	}
	return v
}

// Server is one running serve process.
type Server struct {
	Bin, Port string
	PID       int
	Log       string
	cmd       *exec.Cmd
	done      chan struct{}
}

// FreePort returns a loopback port that was free a moment ago.
func FreePort() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// LogTail returns the last n lines of the file at path.
func LogTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Start runs `bin serve` with env e and waits until it answers. The kill is
// registered before the process starts, so a failed check never leaves it
// running.
func Start(t *testing.T, bin, port string, e Env, logPath string) *Server {
	t.Helper()
	s := &Server{Bin: bin, Port: port, Log: logPath}
	cmd := exec.Command(bin, "serve")
	cmd.Env = e.Vars()
	cmd.Dir = e.Home
	if err := s.startCmd(t, cmd); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	if err := s.Ready(20 * time.Second); err != nil {
		t.Fatalf("%s did not come up: %v", filepath.Base(bin), err)
	}
	return s
}

// startCmd starts cmd with its output appended to s.Log.
func (s *Server) startCmd(t *testing.T, cmd *exec.Cmd) error {
	t.Cleanup(s.Kill)
	f, err := os.OpenFile(s.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "==== %s %s\n", time.Now().UTC().Format(time.RFC3339), strings.Join(cmd.Args, " "))
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	s.cmd, s.PID, s.done = cmd, cmd.Process.Pid, make(chan struct{})
	go func() {
		_ = cmd.Wait()
		f.Close()
		close(s.done)
	}()
	return nil
}

func (s *Server) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Ready waits for GET /login to answer 200 from this process. A process that
// exits first fails at once, with its log, so a stale server on the same port
// is never mistaken for this one.
func (s *Server) Ready(timeout time.Duration) error {
	c := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.exited() {
			return fmt.Errorf("process exited (%v):\n%s", s.cmd.ProcessState, LogTail(s.Log, 40))
		}
		resp, err := c.Get("http://127.0.0.1:" + s.Port + "/login")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && !s.exited() {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no answer on :%s within %v:\n%s", s.Port, timeout, LogTail(s.Log, 40))
}

// Stop sends SIGTERM and waits up to 15 s; a process still running then is
// killed and fails the test.
func (s *Server) Stop(t *testing.T) {
	t.Helper()
	if s.cmd == nil || s.exited() {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		s.Kill()
		t.Errorf("%s ignored SIGTERM for 15 s and was killed", filepath.Base(s.Bin))
	}
}

// Kill stops the process at once. It is idempotent.
func (s *Server) Kill() {
	if s.cmd == nil || s.exited() {
		return
	}
	_ = s.cmd.Process.Kill()
	<-s.done
}

// KillSessions kills the abduco processes recorded under root/sessions/pids
// (format "inner=N srv=M"). A pid is killed only when its command line names
// abduco or root, so a reused pid is never hit. It returns the pids it killed.
func KillSessions(root string) []int {
	files, _ := filepath.Glob(filepath.Join(root, "sessions", "pids", "*.pid"))
	seen := map[int]bool{}
	var killed []int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(b)) {
			_, v, ok := strings.Cut(field, "=")
			pid, err := strconv.Atoi(v)
			if !ok || err != nil || pid <= 1 || seen[pid] {
				continue
			}
			seen[pid] = true
			out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
			if err != nil {
				continue
			}
			cmdline := string(out)
			if !strings.Contains(cmdline, "abduco") && !strings.Contains(cmdline, root) {
				continue
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
				killed = append(killed, pid)
			}
		}
	}
	return killed
}
