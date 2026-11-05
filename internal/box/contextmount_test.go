package box

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// statFake answers the in-box `stat` exec of the context-mount check with out
// (or err) and records each call. The check reads stdout only, so it answers
// Stdout; the exec must be /usr/bin/stat, never a bare stat from the box PATH.
type statFake struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	err   error
}

func (s *statFake) install(f *fakeClient) {
	f.stdoutHook = func(args []string) (string, bool, error) {
		if len(args) < 3 || args[0] != "exec" || args[2] != "/usr/bin/stat" {
			return "", false, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, slices.Clone(args))
		return s.reply(f, args), true, s.err
	}
}

// reply is the stdout of one stat exec, one line per path in argv order: a path
// under /srv/ takes the next line of out, any other (the workspace) is answered
// by the fake client as the box would show it (inBoxStat). A path with no line of
// out left ends the reply there, as a short output does. Caller holds s.mu.
func (s *statFake) reply(f *fakeClient, args []string) string {
	if len(args) < 7 {
		return s.out
	}
	scripted := strings.Split(s.out, "\n")
	var lines []string
	for _, p := range args[6:] {
		if strings.HasPrefix(p, "/srv/") {
			if len(scripted) == 0 || scripted[0] == "" {
				break
			}
			lines = append(lines, scripted[0])
			scripted = scripted[1:]
			continue
		}
		out, err := f.inBoxStat([]string{"exec", args[1], "/usr/bin/stat", "-c", "%d:%i", "--", p})
		if err != nil {
			break
		}
		lines = append(lines, out)
	}
	return strings.Join(lines, "\n")
}

func (s *statFake) set(out string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out, s.err = out, err
}

func (s *statFake) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// wsStat is what the workspace puts at the front of an in-box stat for a
// manager with no VM hook: its path where the host records its identity (Linux),
// nothing where it does not (macOS reads it in the VM, see contextvm_test.go).
func wsStat(dir string) []string {
	if runtime.GOOS == "linux" {
		return []string{dir}
	}
	return nil
}

func ctxMounts(ids ...FileID) []Mount {
	var out []Mount
	for i, id := range ids {
		p := fmt.Sprintf("/srv/ctx%d", i)
		out = append(out, Mount{Host: p, Box: p, ID: &id})
	}
	return out
}

// The three texts a context mount's refusal has, by what the check found: the
// mount of a box this spawn created is not the checked path, the mount of a box
// reused from an earlier spawn is not, or the in-box check did not give an answer.
const (
	changedMsg  = "the mounted path is not the checked one (it changed while the box started); refusing"
	replacedMsg = "the mounted path is not the checked one (it was replaced since the box started); refusing"
)

func TestContextMountMatchStartsTheBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{out: "10:20\n11:21"}
	sf.install(f)
	ms := ctxMounts(FileID{10, 20}, FileID{11, 21})
	dir := ctxTmp(t)
	_, _, name, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err != nil {
		t.Fatal(err)
	}
	want := inBoxStatArgv(name, append(wsStat(dir), "/srv/ctx0", "/srv/ctx1")...)
	if sf.n() != 1 || !slices.Equal(sf.calls[0], want) {
		t.Fatalf("in-box stat calls = %v, want one: %v", sf.calls, want)
	}
	if f.stops() != 0 {
		t.Fatalf("a matching box was stopped")
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live != 1 {
		t.Fatalf("active = %d, want the session counted", live)
	}
}

func TestContextMountMismatchRemovesACreatedBox(t *testing.T) {
	for _, c := range []struct {
		name, out string
		err       error
		bad       string // the path the refusal must name
		want      string // the text after "<what> <path>: "
		// wsFirst: the refusal is the workspace's. A stat that fails as a whole
		// says nothing about which mount is wrong, so the first of the batch is
		// named, and the workspace leads it where the host records its identity.
		wsFirst bool
	}{
		{"second differs", "10:20\n11:99", nil, "/srv/ctx1", changedMsg, false},
		{"first differs", "10:21\n11:21", nil, "/srv/ctx0", changedMsg, false},
		// An answer that is missing or is no identity is a check that did not run,
		// not a path that changed: the text must not claim a change.
		{"short output", "10:20", nil, "/srv/ctx1",
			"the in-box check could not run (it printed no identity for this path); refusing", false},
		{"garbage output", "10:20\nnot an id", nil, "/srv/ctx1",
			"the in-box check could not run (it printed something that is not an identity); refusing", false},
		{"stat failed", "", errors.New("docker exec: exit status 1: stat: cannot statx"), "/srv/ctx0",
			"the in-box check could not run (docker exec: exit status 1: stat: cannot statx); refusing", runtime.GOOS == "linux"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, f := newTestManagerWithFake(t)
			sf := &statFake{out: c.out, err: c.err}
			sf.install(f)
			dir := ctxTmp(t)
			_, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil,
				ctxMounts(FileID{10, 20}, FileID{11, 21}))
			if err == nil {
				t.Fatal("a box whose mount is not the checked path started")
			}
			if c.wsFirst {
				if !errors.Is(err, ErrWorkspaceMountChanged) || !errors.Is(err, ErrBoxUnavailable) {
					t.Fatalf("err = %v, want ErrWorkspaceMountChanged and ErrBoxUnavailable", err)
				}
				if want := "workspace " + dir + ": " + c.want; !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want %q", err, want)
				}
			} else {
				if !errors.Is(err, ErrContextMountChanged) || !errors.Is(err, ErrUnboxableWorkspace) {
					t.Fatalf("err = %v, want ErrContextMountChanged and ErrUnboxableWorkspace", err)
				}
				if want := "context_paths " + c.bad + ": " + c.want; !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want %q", err, want)
				}
			}
			if f.runs() != 1 || f.stops() != 1 {
				t.Fatalf("runs=%d stops=%d, want the created box removed", f.runs(), f.stops())
			}
			for n, live := range m.active {
				if live != 0 {
					t.Fatalf("active[%s] = %d after a refused spawn", n, live)
				}
			}
		})
	}
}

// A box that already runs another session is never removed: the refusal tells
// the user to end those sessions, so the next spawn recreates the box.
func TestContextMountMismatchKeepsALiveBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{out: "10:20"}
	sf.install(f)
	dir := t.TempDir()
	ms := ctxMounts(FileID{10, 20})
	if _, _, _, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms); err != nil {
		t.Fatal(err)
	}
	sf.set("10:77", nil)
	_, _, _, err := m.Wrap(context.Background(), dir, "s2", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err == nil || !errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, want the mount refusal", err)
	}
	if strings.Contains(err.Error(), "the box was removed") {
		t.Fatalf("err = %v, a kept box must not claim it was removed", err)
	}
	if !strings.Contains(err.Error(), "still runs 1 session(s)") || !strings.Contains(err.Error(), "end them so the box is recreated") {
		t.Fatalf("err = %v, want the remedy for a live box", err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("stops=%d runs=%d, want the live box kept and reused", f.stops(), f.runs())
	}
	if sf.n() != 2 {
		t.Fatalf("stat ran %d times, want once per spawn (reuse is checked too)", sf.n())
	}
}

// An idle reused box has nothing to lose: it is removed, so the next spawn
// gets a fresh mount.
func TestContextMountMismatchRemovesAnIdleReusedBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{out: "10:20"}
	sf.install(f)
	dir := t.TempDir()
	ms := ctxMounts(FileID{10, 20})
	_, _, name, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	sf.set("10:77", nil)
	_, _, _, err = m.Wrap(context.Background(), dir, "s2", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err == nil || !errors.Is(err, ErrContextMountChanged) {
		t.Fatalf("err = %v, want the mount refusal", err)
	}
	if !strings.HasSuffix(err.Error(), "; the box was removed, spawn again") {
		t.Fatalf("err = %v, want it to say the idle box was removed", err)
	}
	// A box from an earlier spawn did not change "while the box started".
	if want := "context_paths /srv/ctx0: " + replacedMsg; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if f.runs() != 1 || f.stops() != 1 {
		t.Fatalf("runs=%d stops=%d, want the idle box removed, not recreated", f.runs(), f.stops())
	}
}

// Mounts without an ID (every non-context mount, and context mounts where the
// check is not made) cost no docker call of their own: the one exec lists the
// workspace alone, or nothing where the host records no workspace identity.
func TestContextMountWithoutIDIsNotInTheExec(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	sf := &statFake{}
	sf.install(f)
	dir := ctxTmp(t)
	ms := []Mount{{Host: "/srv/a", Box: "/srv/a"}}
	_, _, name, err := m.Wrap(context.Background(), dir, "s1", BoxKey{Context: "c"}, []string{"claude"}, nil, ms)
	if err != nil {
		t.Fatal(err)
	}
	if ws := wsStat(dir); len(ws) == 0 {
		if sf.n() != 0 {
			t.Fatalf("stat ran %d times for mounts without an ID", sf.n())
		}
	} else if want := inBoxStatArgv(name, ws...); sf.n() != 1 || !slices.Equal(sf.calls[0], want) {
		t.Fatalf("in-box stat calls = %v, want one that lists only the workspace: %v", sf.calls, want)
	}
}

// Off Linux the host records no identity (on macOS the VM read supplies one, see
// contextvm_test.go), so CheckContextPathID itself returns nil.
func TestCheckContextPathIDOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux records the identity (contextid_linux_test.go)")
	}
	d := ctxTmp(t)
	id, err := CheckContextPathID(d, "/h", nil, nil)
	if err != nil || id != nil {
		t.Fatalf("CheckContextPathID = %+v, %v; want nil, nil", id, err)
	}
}

// The refusal text says what the check found, on any platform: a mount of a box
// this spawn created changed while it started, one of a reused box was replaced
// since, a check that did not run claims no change, and a guard is a guard (a file
// or folder), not the workspace. Only a workspace of a created box points to the
// requirement that docker sees host files as they are.
func TestMountChangedTextMatchesTheCase(t *testing.T) {
	const hint = "If this repeats for every folder, CuliBox's docker does not see host files as they are " +
		"(for example Docker Desktop for Linux or a remote docker host); see README, Requirements."
	ws := Mount{Host: "/w/p", kind: mountWorkspace}
	guard := Mount{Host: "/w/p/CLAUDE.md", kind: mountGuard}
	ctx := Mount{Host: "/srv/c"}
	execErr := errors.New("docker exec: exit status 1")
	for _, c := range []struct {
		name    string
		mt      Mount
		created bool
		err     error
		want    string // the whole text
		// classes: every error class the refusal is, and the ones it is not.
		ws, ctxClass bool
	}{
		{"workspace created", ws, true, nil,
			"workspace /w/p: the mounted folder is not the one that was checked (it changed while the box started); refusing. " + hint,
			true, false},
		{"workspace reused", ws, false, nil,
			"workspace /w/p: the mounted folder is not the one that was checked (it was replaced since the box started); refusing",
			true, false},
		{"workspace exec failed on a created box", ws, true, execErr,
			"workspace /w/p: the in-box check could not run (docker exec: exit status 1); refusing", true, false},
		{"workspace exec failed on a reused box", ws, false, execErr,
			"workspace /w/p: the in-box check could not run (docker exec: exit status 1); refusing", true, false},
		{"guard created", guard, true, nil,
			"guard /w/p/CLAUDE.md: the mounted file or folder is not the one that was checked (it changed while the box started); refusing",
			true, false},
		{"guard exec failed", guard, true, execErr,
			"guard /w/p/CLAUDE.md: the in-box check could not run (docker exec: exit status 1); refusing", true, false},
		{"context created", ctx, true, nil,
			"context_paths /srv/c: the mounted path is not the checked one (it changed while the box started); refusing", false, true},
		{"context reused", ctx, false, nil,
			"context_paths /srv/c: the mounted path is not the checked one (it was replaced since the box started); refusing", false, true},
		{"context exec failed", ctx, false, execErr,
			"context_paths /srv/c: the in-box check could not run (docker exec: exit status 1); refusing", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := mountChanged(c.mt, c.created, c.err)
			if err.Error() != c.want {
				t.Fatalf("text:\n got  %q\n want %q", err, c.want)
			}
			if got := errors.Is(err, ErrWorkspaceMountChanged) && errors.Is(err, ErrBoxUnavailable); got != c.ws {
				t.Fatalf("ErrWorkspaceMountChanged and ErrBoxUnavailable = %v, want %v", got, c.ws)
			}
			if got := errors.Is(err, ErrContextMountChanged) && errors.Is(err, ErrUnboxableWorkspace); got != c.ctxClass {
				t.Fatalf("ErrContextMountChanged and ErrUnboxableWorkspace = %v, want %v", got, c.ctxClass)
			}
			if errors.Is(err, ErrContextMountChanged) == c.ws || errors.Is(err, ErrWorkspaceMountChanged) == c.ctxClass {
				t.Fatalf("err = %v is both the workspace and the context refusal", err)
			}
		})
	}
}

// A Wrap batch always starts with the workspace, so the context-only branch of
// identityFailed is not reached from it; this pins it directly. A batch of
// context mounts alone is the context refusal of ctxIdentityFailed, same text,
// and none of the workspace's classes.
func TestIdentityFailedOfContextMountsAloneIsTheContextRefusal(t *testing.T) {
	ms := []Mount{{Host: "/srv/a", Box: "/srv/a"}, {Host: "/srv/b", Box: "/srv/b"}}
	cause := errors.New("path 1: a component is a symlink")
	got := identityFailed(ms, cause)
	want := ctxIdentityFailed([]string{"/srv/a", "/srv/b"}, cause)
	if got.Error() != want.Error() {
		t.Fatalf("text:\n got  %q\n want %q", got, want)
	}
	if wantText := "context_paths /srv/a, /srv/b: the box VM could not confirm the path (path 1: a component is a symlink); refusing"; got.Error() != wantText {
		t.Fatalf("text = %q, want %q", got, wantText)
	}
	for _, class := range []error{ErrContextMountChanged, ErrUnboxableWorkspace} {
		if !errors.Is(got, class) {
			t.Errorf("err = %v, want it to be %v like ctxIdentityFailed's", got, class)
		}
	}
	for _, class := range []error{ErrWorkspaceMountChanged, ErrBoxUnavailable} {
		if errors.Is(got, class) {
			t.Errorf("err = %v, a context-only batch must not be %v", got, class)
		}
	}
}
