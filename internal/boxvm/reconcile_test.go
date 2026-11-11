package boxvm

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/culibox/internal/vmshares"
)

// guestRunner answers `cat /proc/self/mounts` from a table (the guest's real
// state) and records every other ssh command. A remount changes the table the
// way the guest would, unless remountFails or remountNoop is set.
type guestRunner struct {
	fakeRunner
	gmu         sync.Mutex
	table       map[string]string // path -> "ro" | "rw"
	order       []string
	extra       string
	remounts    [][]string // argv of each sh -c call
	remountErr  error
	remountNoop bool
}

func (g *guestRunner) Run(ctx context.Context, env []string, bin string, args ...string) (string, error) {
	g.gmu.Lock()
	defer g.gmu.Unlock()
	if args[0] == "ssh" && args[len(args)-1] == "/proc/self/mounts" {
		var b strings.Builder
		b.WriteString("overlay / overlay rw 0 0\n")
		for _, p := range g.order {
			esc := strings.NewReplacer(" ", `\040`, "\t", `\011`, "\n", `\012`, `\`, `\134`).Replace(p)
			b.WriteString("m " + esc + " virtiofs " + g.table[p] + ",relatime 0 0\n")
		}
		b.WriteString(g.extra)
		return b.String(), nil
	}
	if args[0] == "ssh" && len(args) > 4 && args[len(args)-3] == "sh" {
		g.remounts = append(g.remounts, append([]string(nil), args...))
		if g.remountErr != nil {
			return "sudo: a password is required", g.remountErr
		}
		if !g.remountNoop {
			for _, c := range parseRemounts(args[len(args)-1]) {
				g.table[c.path] = c.mode
			}
		}
		return "", nil
	}
	return g.fakeRunner.Run(ctx, env, bin, args...)
}

type remountCmd struct{ path, mode string }

var (
	encLine   = regexp.MustCompile(`(?m)^p=\$\(printf %s ([A-Za-z0-9+/=]+) \| base64 -d; echo x\); p=\$\{p%x\}$`)
	mountLine = regexp.MustCompile(`(?m)^sudo -n mount -o remount,(ro|rw) -- "\$p"$`)
)

// parseRemounts checks the script has exactly the expected shape (so nothing
// else can ride along) and decodes the paths.
func parseRemounts(script string) []remountCmd {
	es, ms := encLine.FindAllStringSubmatch(script, -1), mountLine.FindAllStringSubmatch(script, -1)
	var out []remountCmd
	for i := range es {
		p, _ := base64.StdEncoding.DecodeString(es[i][1])
		out = append(out, remountCmd{string(p), ms[i][1]})
	}
	return out
}

func reconcileVM(t *testing.T, shares []vmshares.Share, guest map[string]string) (*VM, *guestRunner) {
	t.Helper()
	g := &guestRunner{}
	v := oldVM(t, false, g)
	v.SetShares(shares)
	if err := v.ApplyMounts(context.Background(), ApplyHooks{}); err != nil {
		t.Fatal(err)
	}
	root := v.Config().Root
	g.table = map[string]string{root + "/boxstate": "rw", root + "/bundle": "ro", root + "/broker": "ro", root + "/ca": "ro"}
	g.order = []string{root + "/boxstate", root + "/bundle", root + "/broker", root + "/ca"}
	for _, s := range shares {
		g.table[vmshares.Resolve(s.Path)] = string(s.Mode)
		g.order = append(g.order, vmshares.Resolve(s.Path))
	}
	for p, m := range guest {
		g.table[p] = m
	}
	return v, g
}

func TestReconcileNoMismatchNoRemount(t *testing.T) {
	v, g := reconcileVM(t, newShares, nil)
	if err := v.ReconcileMounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(g.remounts) != 0 {
		t.Fatalf("remounted with nothing wrong: %v", g.remounts)
	}
}

func TestReconcileModeOnlyRemountsBothDirections(t *testing.T) {
	v, g := reconcileVM(t, newShares, map[string]string{"/Users/a/code": "ro", "/Users/a/docs": "rw"})
	if v.VerifyMounts(context.Background()) == nil {
		t.Fatal("setup: guest should be wrong")
	}
	if err := v.ReconcileMounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(g.remounts) != 1 {
		t.Fatalf("want one ssh call, got %v", g.remounts)
	}
	a := g.remounts[0]
	if strings.Join(a[:5], " ") != "ssh --profile agent-box -- sh" || a[5] != "-c" {
		t.Fatalf("argv %q", a)
	}
	got := parseRemounts(a[6])
	want := []remountCmd{{"/Users/a/code", "rw"}, {"/Users/a/docs", "ro"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("remounts %v, want %v", got, want)
	}
	if !strings.HasPrefix(a[6], "set -e\n") {
		t.Fatal("script must stop at the first failure")
	}
}

func TestReconcileRemountScriptIsInertForAwkwardPaths(t *testing.T) {
	paths := []string{
		"/Users/a/with space", "/Users/a/it's", "/Users/a/semi;colon&x|y",
		"/Users/a/-leading", "/Users/a/Tài liệu",
	}
	var shares []vmshares.Share
	guest := map[string]string{}
	for _, p := range paths {
		shares = append(shares, vmshares.Share{Path: p, Mode: vmshares.RO})
		guest[vmshares.Resolve(p)] = "rw"
	}
	v, g := reconcileVM(t, shares, guest)
	if err := v.ReconcileMounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	script := g.remounts[0][6]
	for _, p := range paths {
		for _, frag := range []string{p, "with space", "it's", "semi;"} {
			if strings.Contains(script, frag) {
				t.Fatalf("raw path text %q is in the script:\n%s", frag, script)
			}
		}
	}
	got := parseRemounts(script)
	if len(got) != len(paths) {
		t.Fatalf("decoded %d of %d", len(got), len(paths))
	}
	dec := map[string]bool{}
	for _, c := range got {
		if c.mode != "ro" {
			t.Fatalf("mode %q", c.mode)
		}
		dec[c.path] = true
	}
	for _, p := range paths {
		if !dec[vmshares.Resolve(p)] {
			t.Fatalf("%q not sent intact: %v", p, got)
		}
	}
	// every script line is one of the three fixed shapes
	for _, l := range strings.Split(strings.TrimSpace(script), "\n") {
		if l != "set -e" && !encLine.MatchString(l) && !mountLine.MatchString(l) {
			t.Fatalf("unexpected script line %q", l)
		}
	}
}

// remountScript is also safe for what the config writer would refuse: it must
// not depend on that refusal.
func TestRemountScriptEncodesWhatConfigRefuses(t *testing.T) {
	odd := []string{`/Users/a/My\040Docs`, `/Users/a/dq"x`, "/Users/a/line\nbreak", "/Users/a/$(id)`id`", "/Users/a/'; rm -rf /; '"}
	var ms []Mount
	for _, p := range odd {
		ms = append(ms, Mount{p, false})
	}
	script := remountScript(ms)
	got := parseRemounts(script)
	if len(got) != len(odd) {
		t.Fatalf("decoded %d of %d", len(got), len(odd))
	}
	for i, p := range odd {
		if got[i].path != p || got[i].mode != "ro" {
			t.Fatalf("%d: %v", i, got[i])
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(script), "\n") {
		if l != "set -e" && !encLine.MatchString(l) && !mountLine.MatchString(l) {
			t.Fatalf("unexpected script line %q", l)
		}
	}
}

func TestReconcileMissingOrExtraNeverRemounts(t *testing.T) {
	for name, tweak := range map[string]func(*guestRunner){
		"missing": func(g *guestRunner) {
			delete(g.table, "/Users/a/docs")
			g.order = g.order[:len(g.order)-1]
		},
		"extra": func(g *guestRunner) { g.extra = "m /Users/x virtiofs rw 0 0\n" },
	} {
		t.Run(name, func(t *testing.T) {
			v, g := reconcileVM(t, newShares, map[string]string{"/Users/a/code": "ro"})
			tweak(g)
			err := v.ReconcileMounts(context.Background())
			if !errors.Is(err, ErrMountsMismatch) || !strings.Contains(err.Error(), "/Users/a/code is ro, expected rw") {
				t.Fatalf("err = %v", err)
			}
			if len(g.remounts) != 0 {
				t.Fatalf("remounted: %v", g.remounts)
			}
		})
	}
}

func TestReconcileRemountErrorFailsClosed(t *testing.T) {
	v, g := reconcileVM(t, newShares, map[string]string{"/Users/a/docs": "rw"})
	g.remountErr = errors.New("exit status 1")
	err := v.ReconcileMounts(context.Background())
	if !errors.Is(err, ErrMountsMismatch) || !strings.Contains(err.Error(), "/Users/a/docs is rw, expected ro") || !strings.Contains(err.Error(), "failed: exit status 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestReconcileStillWrongAfterRemountFailsClosed(t *testing.T) {
	v, g := reconcileVM(t, newShares, map[string]string{"/Users/a/docs": "rw"})
	g.remountNoop = true
	err := v.ReconcileMounts(context.Background())
	if !errors.Is(err, ErrMountsMismatch) || !strings.Contains(err.Error(), "/Users/a/docs is rw, expected ro") {
		t.Fatalf("err = %v", err)
	}
	if len(g.remounts) != 1 {
		t.Fatalf("want exactly one remount attempt, got %d", len(g.remounts))
	}
}

func TestVerifyMountsNeverRemounts(t *testing.T) {
	v, g := reconcileVM(t, newShares, map[string]string{"/Users/a/docs": "rw"})
	if err := v.VerifyMounts(context.Background()); !errors.Is(err, ErrMountsMismatch) {
		t.Fatal(err)
	}
	if len(g.remounts) != 0 {
		t.Fatal("VerifyMounts must stay read-only")
	}
}
