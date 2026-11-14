//go:build upgrade

package upgradetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServeEnvIsExplicit(t *testing.T) {
	t.Setenv("CULIBOX_HOME", "/x")
	t.Setenv("AGENT_BOX_HOME", "/y")
	t.Setenv("CLAUDE_CONFIG_DIR", "/z")
	keys := func(vars []string) []string {
		var k []string
		for _, v := range vars {
			name, _, _ := strings.Cut(v, "=")
			k = append(k, name)
		}
		sort.Strings(k)
		return k
	}
	vars := Env{Home: "/tmp/abupX/h"}.Vars()
	if got, want := strings.Join(keys(vars), ","), "HOME,LANG,PATH,SHELL,TMPDIR,USER"; got != want {
		t.Fatalf("keys %s, want %s", got, want)
	}
	for _, v := range vars {
		if strings.HasPrefix(v, "HOME=") && v != "HOME=/tmp/abupX/h" {
			t.Fatalf("HOME is %q", v)
		}
	}
	vars = Env{Home: "/tmp/abupX/h", RootEnv: "/tmp/abupX/h/r"}.Vars()
	n := 0
	for _, v := range vars {
		if strings.HasPrefix(v, "AGENT_BOX_HOME=") {
			n++
			if v != "AGENT_BOX_HOME=/tmp/abupX/h/r" {
				t.Fatalf("root env %q", v)
			}
		}
		if strings.HasPrefix(v, "CULIBOX_HOME=") || strings.HasPrefix(v, "CLAUDE_") {
			t.Fatalf("leaked %q", v)
		}
	}
	if n != 1 {
		t.Fatalf("AGENT_BOX_HOME set %d times", n)
	}
}

func TestEnvRefusesHomeOutsideTemp(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Vars accepted a HOME outside /tmp/abup*")
		}
	}()
	Env{Home: os.Getenv("HOME")}.Vars()
}

func TestReadyFailsWhenProcessExited(t *testing.T) {
	log := filepath.Join(t.TempDir(), "serve.log")
	s := &Server{Port: FreePort(), Log: log}
	cmd := exec.Command("/bin/sh", "-c", "echo boom; exit 3")
	if err := s.startCmd(t, cmd); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := s.Ready(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Ready: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Ready took %v after the process exited", d)
	}
}

func TestCleanupKillsOnlyRecordedPIDs(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "abup")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	cmd := exec.Command("/bin/sh", "-c", "sleep 30", root)
	// Its own process group, so the sleep the shell forks is killed with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	pid := cmd.Process.Pid
	writeFile(t, filepath.Join(root, "sessions/pids/a.pid"), fmt.Sprintf("inner=%d srv=%d\n", pid, pid), 0o600)
	writeFile(t, filepath.Join(root, "sessions/pids/b.pid"), "inner=1 srv=1\n", 0o600)
	killed := KillSessions(root)
	if len(killed) != 1 || killed[0] != pid {
		t.Fatalf("killed %v, want [%d]", killed, pid)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the recorded process is still running")
	}
	if err := syscall.Kill(1, 0); err != nil && err != syscall.EPERM {
		t.Fatalf("pid 1: %v", err)
	}
}

func TestClientSendsOriginAndBearer(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, HTTP: srv.Client()}
	var out struct{ OK bool }
	code, err := c.Do("POST", "/api/x", map[string]string{"a": "b"}, &out)
	if err != nil || code != 200 || !out.OK {
		t.Fatalf("Do: %d %v %+v", code, err, out)
	}
	if got.Get("Origin") != srv.URL || got.Get("Authorization") != "" || got.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", got)
	}
	b := c.WithBearer("x")
	if b.HTTP.Jar != nil {
		t.Fatal("bearer client carries the cookie jar")
	}
	if _, err := b.Do("GET", "/api/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer x" {
		t.Fatalf("Authorization %q", got.Get("Authorization"))
	}
}

func TestReadyRejectsAForeignServer(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer foreign.Close()
	port := foreign.URL[strings.LastIndex(foreign.URL, ":")+1:]
	s := &Server{Port: port, Log: filepath.Join(t.TempDir(), "serve.log")}
	cmd := exec.Command("sleep", "5")
	if err := s.startCmd(t, cmd); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(1500 * time.Millisecond); err == nil {
		t.Fatal("Ready accepted a server that is not this process")
	}
}

func TestReadyAcceptsOurListeningLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	log := filepath.Join(t.TempDir(), "serve.log")
	writeFile(t, log, "x culibox listening on 127.0.0.1:"+port+" (old run)\n", 0o600)
	s := &Server{Port: port, Log: log}
	// The line before this start's marker must not count; the one after must.
	if err := s.startCmd(t, exec.Command("sh", "-c", "sleep 0.3; printf '%s listening on 127.0.0.1:%s (allowed hosts: x)\\n' y \"$0\"; exec sleep 5", port)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Ready(3 * time.Second); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("Ready returned after %v, before the process logged its listening line", d)
	}
}
