package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/bugreport"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/doctor"
	"github.com/culiops/agent-box/internal/session"
)

func testBugDeps() bugDeps {
	return bugDeps{
		checks:  func() []doctor.Result { return []doctor.Result{{Status: "ok", Check: "home", Detail: "fine"}} },
		timeout: time.Second,
		start:   time.Now().Add(-90 * time.Second),
		base: bugreport.Input{Producer: "daemon", Commit: "abc1234", Home: "/home/u", Host: "box-1",
			Config: config.Config{}},
		profiles: func() []config.AgentConfig { return []config.AgentConfig{{Name: "claude", Bin: "claude"}} },
		sessions: func() []session.Info { return nil },
		logs:     func() []string { return []string{"started"} },
	}
}

func TestBuildBugReportTimesOutDoctor(t *testing.T) {
	d := testBugDeps()
	d.timeout = 50 * time.Millisecond
	d.checks = func() []doctor.Result { time.Sleep(2 * time.Second); return nil }
	t0 := time.Now()
	md, url := buildBugReport(d)(context.Background())
	if el := time.Since(t0); el > 500*time.Millisecond {
		t.Fatalf("took %v, want about the 50ms timeout", el)
	}
	if !strings.Contains(md, "doctor timed out") {
		t.Errorf("markdown lacks the timeout note:\n%s", md)
	}
	if !strings.HasPrefix(url, "https://") {
		t.Errorf("issue url = %q", url)
	}
}

func TestBuildBugReportIncludesLiveState(t *testing.T) {
	d := testBugDeps()
	d.sessions = func() []session.Info {
		return []session.Info{
			{Runtime: "box", Status: "working"},
			{Runtime: "box", Status: "working"},
			{Runtime: "", Status: "waiting"},
			{Runtime: "host", Status: "idle", Exited: true},
		}
	}
	md, _ := buildBugReport(d)(context.Background())
	for _, want := range []string{"agent-box bug report v1", "producer: daemon", "box/working", "host/waiting", "host/exited", "started", "abc1234"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestBuildBugReportRedactsLogs(t *testing.T) {
	d := testBugDeps()
	d.logs = func() []string { return []string{"opened /home/u/secret on box-1"} }
	md, _ := buildBugReport(d)(context.Background())
	if strings.Contains(md, "/home/u/") || strings.Contains(md, "box-1") {
		t.Errorf("home or host leaked:\n%s", md)
	}
}

func TestBuildBugReportDoctorPanicIsANote(t *testing.T) {
	d := testBugDeps()
	d.checks = func() []doctor.Result { panic("boom") }
	md, _ := buildBugReport(d)(context.Background())
	if !strings.Contains(md, "doctor failed") {
		t.Errorf("a panicking check must become a note, not crash the daemon:\n%s", md)
	}
}

func TestBugSessionCounts(t *testing.T) {
	got := bugSessionCounts([]session.Info{
		{Runtime: "box", Status: "working"}, {Runtime: "", Status: "idle"}, {Runtime: "box", Status: "x", Exited: true},
	})
	want := map[string]int{"box/working": 1, "host/idle": 1, "box/exited": 1}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got %v want %v", got, want)
		}
	}
}
