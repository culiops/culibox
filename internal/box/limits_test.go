package box

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestValidateLimits(t *testing.T) {
	ok := []struct {
		pids     int
		tmp, mem string
	}{{0, "", ""}, {512, "512m", "1g"}, {4096, "4g", "2048m"}, {0, "", "999999g"}}
	for _, c := range ok {
		if err := ValidateLimits(c.pids, c.tmp, c.mem); err != nil {
			t.Errorf("ValidateLimits(%d,%q,%q) = %v, want ok", c.pids, c.tmp, c.mem, err)
		}
	}
	bad := []struct {
		pids     int
		tmp, mem string
	}{{63, "", ""}, {65537, "", ""}, {-1, "", ""}, {0, "512", ""}, {0, "0m", ""}, {0, "512m,suid", ""}, {0, "1t", ""}, {0, "1234567m", ""}, {0, " 512m", ""},
		{0, "", "512m"}, {0, "", "1023m"}, {0, "", "999999k"}, {0, "", "1048576k"}, {0, "", "1.5g"}, {0, "", "3G"}, {0, "", "2048"}, {0, "", "0g"},
		{0, "", "3g,suid"}, {0, "", " 3g"}, {0, "", "1t"}, {0, "", "1234567m"}}
	for _, c := range bad {
		if err := ValidateLimits(c.pids, c.tmp, c.mem); err == nil {
			t.Errorf("ValidateLimits(%d,%q,%q) = nil, want an error", c.pids, c.tmp, c.mem)
		}
	}
	if err := ValidateLimits(0, "", "512m"); err == nil || !strings.Contains(err.Error(), "box_memory") {
		t.Errorf("a memory error must name box_memory, got %v", err)
	}
}

func TestDefaultMemoryIsValid(t *testing.T) {
	if err := ValidateLimits(0, "", DefaultMemory); err != nil {
		t.Fatalf("DefaultMemory %q must pass its own validation: %v", DefaultMemory, err)
	}
}

func TestRunArgsUseConfiguredLimits(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	m.opts.PidsLimit, m.opts.TmpSize = 2048, "2g"
	got := m.runArgs("ab-box-abc", "/w", nil, false, false, "", "3g", "", nil)
	if !containsPair(got, "--pids-limit", "2048") {
		t.Errorf("pids limit not applied: %q", got)
	}
	if !containsPair(got, "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=2g") {
		t.Errorf("tmp size not applied: %q", got)
	}
	if !containsPair(got, "--memory", "3g") || !containsPair(got, "--memory-swap", "3g") {
		t.Errorf("memory cap (swap equal to it) not applied: %q", got)
	}
	if slices.Contains(got, "--cpus") {
		t.Errorf("no box_cpus must mean no --cpus flag: %q", got)
	}

	m.opts.PidsLimit, m.opts.TmpSize = 0, ""
	got = m.runArgs("ab-box-abc", "/w", nil, false, false, "", DefaultMemory, "", nil)
	if !containsPair(got, "--pids-limit", "512") || !containsPair(got, "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m") {
		t.Errorf("zero values must give the defaults: %q", got)
	}
}

func TestRunArgsCPUs(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	for _, c := range []struct {
		in   float64
		want string
	}{{2, "2"}, {0.5, "0.5"}, {1.25, "1.25"}, {1.1, "1.1"}, {1024, "1024"}} {
		m.opts.CPUs = c.in
		if got := m.runArgs("ab-box-abc", "/w", nil, false, false, "", "4g", "", nil); !containsPair(got, "--cpus", c.want) {
			t.Errorf("CPUs %v: want --cpus %s in %q", c.in, c.want, got)
		}
	}
}

func TestValidateCPUs(t *testing.T) {
	for _, ok := range []float64{0, 0.01, 0.5, 1, 1.25, 2, 1.1, 64, 1024} {
		if err := ValidateCPUs(ok); err != nil {
			t.Errorf("ValidateCPUs(%v) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []float64{-1, 0.001, 0.005, 1.234, 1024.01, 2000, math.NaN(), math.Inf(1)} {
		err := ValidateCPUs(bad)
		if err == nil || !strings.Contains(err.Error(), "box_cpus") {
			t.Errorf("ValidateCPUs(%v) = %v, want an error naming box_cpus", bad, err)
		}
	}
}

func TestValidateLimitsMax(t *testing.T) {
	if err := ValidateLimits(0, "", "max"); err != nil {
		t.Errorf(`box_memory "max" must be valid: %v`, err)
	}
	for _, bad := range []string{"MAX", "Max", "max ", "maximum"} {
		if err := ValidateLimits(0, "", bad); err == nil {
			t.Errorf("ValidateLimits(%q) = nil, want an error", bad)
		}
	}
}

// memInfo makes the fake answer `docker info --format {{.MemTotal}}`.
func memInfo(f *fakeClient, calls *int, out string, err error) {
	f.stdoutHook = func(args []string) (string, bool, error) {
		if slices.Equal(args, []string{"info", "--format", "{{.MemTotal}}"}) {
			*calls++
			return out, true, err
		}
		return "", false, nil
	}
}

func TestMemoryCapExplicitNeverAsksDocker(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	var calls int
	memInfo(f, &calls, "1", nil)
	m.opts.Memory = "2048m"
	got, err := m.memoryCap(context.Background())
	if err != nil || got != "2048m" || calls != 0 {
		t.Errorf("explicit size: got %q, %v, %d docker calls", got, err, calls)
	}
}

func TestMemoryCapDefaultIsHalfOfDocker(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		total string
		want  string
	}{
		{strconv.Itoa(8 * gib), "4096m"},
		{"11800000001", "5626m"},         // 11800000001/2 = 5900000000 B = 5626.7 MiB, rounded down
		{strconv.Itoa(6 * gib), "4096m"}, // half is 3g: the floor is min(4g, MemTotal) = 4g
		{strconv.Itoa(4 * gib), "4096m"}, // a 4 GiB Mac VM: boxes keep the old 4g, never half of it
		{strconv.Itoa(2 * gib), "2048m"}, // below 4g in total: all of it, not 1g
		{strconv.Itoa(gib + gib/2), "1536m"},
	} {
		m, f := newTestManagerWithFake(t)
		var calls int
		memInfo(f, &calls, c.total, nil)
		got, err := m.memoryCap(context.Background())
		if err != nil || got != c.want {
			t.Errorf("MemTotal %s: got %q, %v, want %q", c.total, got, err, c.want)
		}
	}
}

func TestMemoryCapMaxIsAllOfDocker(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	var calls int
	memInfo(f, &calls, strconv.Itoa(8<<30), nil)
	m.opts.Memory = "max"
	got, err := m.memoryCap(context.Background())
	if err != nil || got != "8192m" {
		t.Errorf("max: got %q, %v", got, err)
	}
}

func TestMemoryCapReadIsCachedOnSuccess(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	var calls int
	memInfo(f, &calls, strconv.Itoa(8<<30), nil)
	for i := 0; i < 3; i++ {
		if _, err := m.memoryCap(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("a successful read must be cached: %d docker calls", calls)
	}
}

func TestMemoryCapFallsBackAndRetries(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	for name, bad := range map[string]struct {
		out string
		err error
	}{"error": {"", errors.New("daemon down")}, "zero": {"0", nil}, "junk": {"lots", nil}} {
		logs.Reset()
		m, f := newTestManagerWithFake(t)
		var calls int
		memInfo(f, &calls, bad.out, bad.err)
		for i := 0; i < 2; i++ {
			got, err := m.memoryCap(context.Background())
			if err != nil || got != DefaultMemory {
				t.Errorf("%s: unreadable MemTotal must fall back to %s, got %q, %v", name, DefaultMemory, got, err)
			}
		}
		if calls != 2 {
			t.Errorf("%s: a failure must not be cached (the VM may not be up yet): %d calls", name, calls)
		}
		if n := strings.Count(logs.String(), "box_memory"); n != 1 {
			t.Errorf("%s: the fallback must be logged once, logged %d times: %q", name, n, logs.String())
		}
		// the VM comes up: the next call reads the real value
		memInfo(f, &calls, strconv.Itoa(8<<30), nil)
		if got, err := m.memoryCap(context.Background()); err != nil || got != "4096m" {
			t.Errorf("%s: after recovery got %q, %v, want 4096m", name, got, err)
		}
	}
}

func TestMemoryCapMaxFailsClosed(t *testing.T) {
	for _, bad := range []struct {
		out string
		err error
	}{{"", errors.New("daemon down")}, {"0", nil}} {
		m, f := newTestManagerWithFake(t)
		var calls int
		memInfo(f, &calls, bad.out, bad.err)
		m.opts.Memory = "max"
		got, err := m.memoryCap(context.Background())
		if err == nil || got != "" || !strings.Contains(err.Error(), "box_memory") {
			t.Errorf("max with an unreadable MemTotal must error naming box_memory, got %q, %v", got, err)
		}
	}
}

// A deleted pinned tag must fail the spawn, not be fetched from Docker Hub.
func TestBoxRunsNeverPull(t *testing.T) {
	m := newTestManager(t)
	got := m.runArgs("ab-box-abc", "/w", nil, false, false, "", DefaultMemory, "", nil)
	p := slices.Index(got, "--pull=never")
	if p < 0 {
		t.Fatalf("box run does not carry --pull=never: %q", got)
	}
	if img := slices.Index(got, m.opts.Image); img < 0 || p > img {
		t.Fatalf("--pull=never (%d) must come before the image (%d): %q", p, img, got)
	}
}

func TestNewRefusesBadLimits(t *testing.T) {
	_, err := New(Options{
		DockerBin: "/usr/bin/docker", Image: "culiops/agent-box:base", Instance: "TEST",
		StateRoot: t.TempDir(), Client: newFake(), TmpSize: "1g,suid",
	})
	if err == nil {
		t.Fatal("a tmp size that adds mount options must be refused")
	}
	_, err = New(Options{
		DockerBin: "/usr/bin/docker", Image: "culiops/agent-box:base", Instance: "TEST",
		StateRoot: t.TempDir(), Client: newFake(), Memory: "512m",
	})
	if err == nil {
		t.Fatal("a memory cap below 1g must be refused")
	}
}

func TestWrapAppliesResolvedMemory(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	var calls int
	memInfo(f, &calls, strconv.Itoa(8<<30), nil)
	m.opts.CPUs = 2
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"sh"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	got := f.runArgs[len(f.runArgs)-1]
	if !containsPair(got, "--memory", "4096m") || !containsPair(got, "--memory-swap", "4096m") || !containsPair(got, "--cpus", "2") {
		t.Errorf("unset box_memory must be half of docker's 8 GiB, and box_cpus applied: %q", got)
	}
}

func TestWrapMaxMemoryUnreadableRefusesSpawn(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	var calls int
	memInfo(f, &calls, "", errors.New("daemon down"))
	m.opts.Memory = MemoryMax
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"sh"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "box_memory") {
		t.Fatalf("want ErrBoxUnavailable naming box_memory, got %v", err)
	}
	if len(f.runArgs) != 0 {
		t.Errorf("a refused spawn must not start a box: %q", f.runArgs)
	}
}
