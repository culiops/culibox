package boxvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/vmshares"
)

// oldVM is a VM provisioned the v0.32 way: the YAML and stamp are written for
// the whole-home mount list, with no Shares.
func oldVM(t *testing.T, insideHome bool, r Runner) *VM {
	t.Helper()
	root := shortRoot(t)
	c := Config{Root: root, Home: "/Users/a", ColimaBin: "/c", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}
	mounts := []Mount{{"/Users/a", true}, {root, true}}
	if insideHome {
		c.Home = root
		c.Root = filepath.Join(root, "ab")
		mounts = []Mount{{root, true}}
	}
	v := NewVM(c, r)
	c = v.Config()
	for i := range mounts { // match the resolved spelling
		mounts[i].Location = vmshares.Resolve(mounts[i].Location)
	}
	y, err := renderYAML(c, mounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.ProfileDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.YAMLPath(), y, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.StampPath(), []byte(yamlHash(y)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return v
}

var newShares = []vmshares.Share{{Path: "/Users/a/code", Mode: vmshares.RW}, {Path: "/Users/a/docs", Mode: vmshares.RO}}

func TestDriftKindOldWholeHomeVMIsMountsOnly(t *testing.T) {
	for name, inside := range map[string]bool{"data root outside home (two mounts)": false, "data root inside home (one mount)": true} {
		t.Run(name, func(t *testing.T) {
			v := oldVM(t, inside, &fakeRunner{})
			// The v0.32 file is the stamp: its mounts are home-wide, so with
			// no shares set the new config differs only by mounts.
			if k, err := v.DriftKind(); err != nil || k != DriftMountsOnly {
				t.Fatalf("old VM: %v %v", k, err)
			}
			if wh, err := v.WholeHome(); err != nil || !wh {
				t.Fatalf("WholeHome = %v %v", wh, err)
			}
			v.SetShares(newShares)
			if k, err := v.DriftKind(); err != nil || k != DriftMountsOnly {
				t.Fatalf("shares changed: %v %v", k, err)
			}
			v.cfg.CPUs = 8
			if k, err := v.DriftKind(); err != nil || k != DriftOther {
				t.Fatalf("cpus changed: %v %v", k, err)
			}
		})
	}
}

func TestDriftKindNoneAndNotSetUp(t *testing.T) {
	v := NewVM(Config{Root: shortRoot(t), Home: "/h", ColimaBin: "/c", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}, &fakeRunner{})
	if _, err := v.DriftKind(); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("want ErrNotSetUp, got %v", err)
	}
	r := &fakeRunner{}
	v = setUpVM(t, r)
	if k, err := v.DriftKind(); err != nil || k != DriftNone {
		t.Fatalf("fresh: %v %v", k, err)
	}
	if wh, err := v.WholeHome(); err != nil || wh { // Home == Root in setUpVM, but only subfolders mount
		t.Fatalf("WholeHome on a fresh VM = %v %v", wh, err)
	}
}

func callNames(r *fakeRunner) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, strings.Fields(c)[0])
	}
	return out
}

func TestApplyMountsStopsWritesStarts(t *testing.T) {
	r := &fakeRunner{}
	v := oldVM(t, false, r)
	r.running.Store(true)
	v.SetShares(newShares)
	var steps []string
	h := ApplyHooks{Step: func(s ApplyStep, e StepEvent) {
		steps = append(steps, map[ApplyStep]string{StepStop: "stop", StepStart: "start"}[s]+map[bool]string{false: ":begin", true: ":done"}[e.Done])
	}}
	if err := v.ApplyMounts(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(callNames(r), ","); got != "status,stop,start" {
		t.Fatalf("calls = %s", got)
	}
	if got := strings.Join(steps, ","); got != "stop:begin,stop:done,start:begin,start:done" {
		t.Fatalf("steps = %s", got)
	}
	c := v.Config()
	got, err := ReadMounts(c.YAMLPath())
	if err != nil || len(got) != 6 || got[4] != (Mount{"/Users/a/code", true}) || got[5] != (Mount{"/Users/a/docs", false}) {
		t.Fatalf("mounts = %v %v", got, err)
	}
	y, _ := c.YAML()
	if b, _ := os.ReadFile(c.StampPath()); strings.TrimSpace(string(b)) != yamlHash(y) {
		t.Fatal("stamp is not the new YAML hash")
	}
	for _, m := range DataMounts(c.Root) {
		if fi, err := os.Stat(m.Location); err != nil || !fi.IsDir() {
			t.Fatalf("data dir %s missing", m.Location)
		}
	}
	if k, _ := v.DriftKind(); k != DriftNone {
		t.Fatalf("after apply drift = %v", k)
	}
	if wh, _ := v.WholeHome(); wh {
		t.Fatal("home is still mounted after apply")
	}
}

func TestApplyMountsVMStoppedSkipsStop(t *testing.T) {
	r := &fakeRunner{}
	v := oldVM(t, true, r)
	v.SetShares(newShares)
	if err := v.ApplyMounts(context.Background(), ApplyHooks{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(callNames(r), ","); got != "status,start" {
		t.Fatalf("calls = %s", got)
	}
}

func TestApplyMountsRefusesOtherDrift(t *testing.T) {
	r := &fakeRunner{}
	v := oldVM(t, false, r)
	v.cfg.CPUs = 8
	before, _ := os.ReadFile(v.Config().YAMLPath())
	err := v.ApplyMounts(context.Background(), ApplyHooks{})
	if !errors.Is(err, ErrNeedsRecreate) || !strings.Contains(err.Error(), "agent-box box setup -recreate") {
		t.Fatalf("err = %v", err)
	}
	if len(callNames(r)) != 0 {
		t.Fatalf("runner called: %v", callNames(r))
	}
	if after, _ := os.ReadFile(v.Config().YAMLPath()); string(after) != string(before) {
		t.Fatal("yaml rewritten despite refusal")
	}
}

func TestApplyMountsStartTimeout(t *testing.T) {
	r := &fakeRunner{delay: time.Second}
	v := oldVM(t, false, r)
	v.startTimeout = 20 * time.Millisecond
	v.SetShares(newShares)
	oldStamp, _ := os.ReadFile(v.Config().StampPath())
	err := v.ApplyMounts(context.Background(), ApplyHooks{})
	if !errors.Is(err, ErrStartTimeout) || err.Error() != "The VM did not start within 2 minutes." {
		t.Fatalf("err = %v", err)
	}
	ms, _ := ReadMounts(v.Config().YAMLPath())
	if len(ms) != 6 {
		t.Fatalf("yaml not rewritten: %v", ms)
	}
	if s, _ := os.ReadFile(v.Config().StampPath()); string(s) != string(oldStamp) {
		t.Fatal("stamp written after a failed start")
	}
}

func TestApplyMountsStopFailureWritesNothing(t *testing.T) {
	r := &fakeRunner{failOn: "stop"}
	v := oldVM(t, false, r)
	r.running.Store(true)
	v.SetShares(newShares)
	var doneErr error
	err := v.ApplyMounts(context.Background(), ApplyHooks{Step: func(s ApplyStep, e StepEvent) {
		if s == StepStop && e.Done {
			doneErr = e.Err
		}
	}})
	if err == nil || doneErr == nil {
		t.Fatalf("err = %v, hook err = %v", err, doneErr)
	}
	if ms, _ := ReadMounts(v.Config().YAMLPath()); len(ms) != 2 {
		t.Fatalf("yaml changed after a failed stop: %v", ms)
	}
}

func TestApplyMountsNeverDeletes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		failOn  string
		delay   time.Duration
		cpus    int
	}{
		{"running", true, "", 0, 0}, {"stopped", false, "", 0, 0}, {"stop fails", true, "stop", 0, 0},
		{"start fails", false, "start", 0, 0}, {"timeout", false, "", time.Second, 0}, {"other drift", true, "", 0, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{failOn: tc.failOn, delay: tc.delay}
			v := oldVM(t, false, r)
			r.running.Store(tc.running)
			if tc.delay > 0 {
				v.startTimeout = 10 * time.Millisecond
			}
			if tc.cpus > 0 {
				v.cfg.CPUs = tc.cpus
			}
			v.SetShares(newShares)
			_ = v.ApplyMounts(context.Background(), ApplyHooks{})
			for _, n := range callNames(r) {
				if n == "delete" {
					t.Fatal("ApplyMounts ran colima delete")
				}
			}
		})
	}
}

func TestAppliedMountsCacheInvalidatesOnWrite(t *testing.T) {
	v := oldVM(t, false, &fakeRunner{})
	a, err := v.AppliedMounts()
	if err != nil || len(a) != 2 {
		t.Fatalf("first read = %v %v", a, err)
	}
	a[0].Location = "mutated" // a caller's copy must not poison the cache
	if b, _ := v.AppliedMounts(); b[0].Location == "mutated" {
		t.Fatal("cache returned a shared slice")
	}
	v.SetShares(newShares)
	if err := v.ApplyMounts(context.Background(), ApplyHooks{}); err != nil {
		t.Fatal(err)
	}
	if c, err := v.AppliedMounts(); err != nil || len(c) != 6 {
		t.Fatalf("after write = %v %v", c, err)
	}
}

func TestVerifyMountsExactSet(t *testing.T) {
	r := &fakeRunner{}
	v := oldVM(t, false, r)
	v.SetShares(newShares)
	if err := v.ApplyMounts(context.Background(), ApplyHooks{}); err != nil {
		t.Fatal(err)
	}
	root := v.Config().Root
	exact := "overlay / overlay rw 0 0\n" +
		"m0 " + root + "/boxstate virtiofs rw,relatime 0 0\n" +
		"m1 " + root + "/bundle virtiofs ro,relatime 0 0\n" +
		"m2 " + root + "/broker virtiofs ro,relatime 0 0\n" +
		"m3 " + root + "/ca virtiofs ro,relatime 0 0\n" +
		"m4 /Users/a/code virtiofs rw,relatime 0 0\n" +
		"m5 /Users/a/docs virtiofs ro,relatime 0 0\n"
	r.mountsOut = exact
	if err := v.VerifyMounts(context.Background()); err != nil {
		t.Fatalf("exact: %v", err)
	}
	r.mountsOut = exact + "m6 /Users/x virtiofs rw,relatime 0 0\n"
	if err := v.VerifyMounts(context.Background()); !errors.Is(err, ErrMountsMismatch) || !strings.Contains(err.Error(), "/Users/x") {
		t.Fatalf("extra: %v", err)
	}
	r.mountsOut = strings.Replace(exact, "m5 /Users/a/docs virtiofs ro", "m5 /Users/a/docs virtiofs rw", 1)
	if err := v.VerifyMounts(context.Background()); !errors.Is(err, ErrMountsMismatch) || !strings.Contains(err.Error(), "/Users/a/docs") {
		t.Fatalf("flipped: %v", err)
	}
	r.mountsOut = strings.Replace(exact, "m4 /Users/a/code virtiofs rw,relatime 0 0\n", "", 1)
	if err := v.VerifyMounts(context.Background()); !errors.Is(err, ErrMountsMismatch) || !strings.Contains(err.Error(), "missing /Users/a/code") {
		t.Fatalf("missing: %v", err)
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/Users/a/My\040Docs`); got != "/Users/a/My Docs" {
		t.Fatal(got)
	}
	if got := unescapeMount(`/x\04`); got != `/x\04` {
		t.Fatal(got)
	}
}
