package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/bugreport"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/doctor"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/web"
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
	md, url, _ := buildBugReport(d)(context.Background())
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
	md, _, _ := buildBugReport(d)(context.Background())
	for _, want := range []string{"agent-box bug report v1", "producer: daemon", "box/working", "host/waiting", "host/exited", "started", "abc1234"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestBuildBugReportRedactsLogs(t *testing.T) {
	d := testBugDeps()
	d.logs = func() []string { return []string{"opened /home/u/secret on box-1"} }
	md, _, _ := buildBugReport(d)(context.Background())
	if strings.Contains(md, "/home/u/") || strings.Contains(md, "box-1") {
		t.Errorf("home or host leaked:\n%s", md)
	}
}

func TestBuildBugReportDoctorPanicIsANote(t *testing.T) {
	d := testBugDeps()
	d.checks = func() []doctor.Result { panic("boom") }
	md, _, _ := buildBugReport(d)(context.Background())
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

// A cancelled request stops waiting, not the doctor run behind it; the slot
// stays taken until that run returns, so cancel cycles cannot stack runs.
func TestBuildBugReportCancelKeepsSlotUntilDoctorEnds(t *testing.T) {
	d := testBugDeps()
	d.timeout = 5 * time.Second
	started, release := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	d.checks = func() []doctor.Result {
		runs.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}
	build := buildBugReport(d)

	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		md  string
		err error
	}
	done := make(chan res, 1)
	go func() {
		md, _, err := build(ctx)
		done <- res{md, err}
	}()
	<-started
	cancel()
	r := <-done
	if r.err != nil || !strings.Contains(r.md, "doctor was cancelled") {
		t.Fatalf("cancelled request: err=%v md lacks the cancelled note:\n%s", r.err, r.md)
	}

	// The handler has returned, but doctor is still running: refuse, do not start another.
	for i := 0; i < 3; i++ {
		if _, _, err := build(context.Background()); !errors.Is(err, web.ErrBugReportBusy) {
			t.Fatalf("request %d while doctor still runs: err=%v, want ErrBugReportBusy", i, err)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("doctor started %d times, want 1", n)
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, err := build(context.Background())
		if err == nil {
			break
		}
		if !errors.Is(err, web.ErrBugReportBusy) || time.Now().After(deadline) {
			t.Fatalf("slot never freed after doctor ended: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A timed-out request also leaves the slot taken while its doctor is running.
func TestBuildBugReportTimeoutKeepsSlotUntilDoctorEnds(t *testing.T) {
	d := testBugDeps()
	d.timeout = 20 * time.Millisecond
	release := make(chan struct{})
	d.checks = func() []doctor.Result { <-release; return nil }
	build := buildBugReport(d)
	if _, _, err := build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := build(context.Background()); !errors.Is(err, web.ErrBugReportBusy) {
		t.Fatalf("err=%v, want ErrBugReportBusy", err)
	}
	close(release)
}

func TestBuildBugReportPanicFreesSlot(t *testing.T) {
	d := testBugDeps()
	d.checks = func() []doctor.Result { panic("boom") }
	build := buildBugReport(d)
	if _, _, err := build(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, err := build(context.Background()); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a panicking doctor run kept the slot forever")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
