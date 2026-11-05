package boxvm

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// sshRunner records the exact argv of each call (fakeRunner joins it with
// spaces, which would hide a quoting bug) and answers with out/err.
type sshRunner struct {
	mu   sync.Mutex
	argv [][]string
	envs [][]string
	out  string
	err  error
}

func (s *sshRunner) Run(_ context.Context, env []string, _ string, args ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.argv = append(s.argv, slices.Clone(args))
	s.envs = append(s.envs, env)
	return s.out, s.err
}

// awkwardPaths are paths whose bytes mean something to a shell.
var awkwardPaths = []string{
	"/Users/x/plain",
	"/Users/x/with space",
	"/Users/x/$HOME and $(id)",
	"/Users/x/it's",
	"/Users/x/back`tick`",
	"/Users/x/Tài liệu Việt Nam",
	"/Users/x/semi;colon&amp|pipe\"dq\\bs",
}

var base64Word = regexp.MustCompile(`^[A-Za-z0-9+/=]+$`)

// listWords pulls the words after `for e in` out of a PathIDs script.
func listWords(t *testing.T, script string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^for e in (.*); do$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("no `for e in <words>; do` line in the script:\n%s", script)
	}
	return strings.Fields(m[1])
}

func TestPathIDsRunsOneFixedScriptOverColimaSSH(t *testing.T) {
	r := &sshRunner{out: strings.Repeat("ctxid 44:393\n", len(awkwardPaths))}
	v := vmWith(t, r)
	r.argv, r.envs = nil, nil
	ids, err := v.PathIDs(context.Background(), awkwardPaths)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(awkwardPaths) || ids[0] != (FileID{Dev: 44, Ino: 393}) {
		t.Fatalf("ids = %+v", ids)
	}
	if len(r.argv) != 1 {
		t.Fatalf("want exactly one VM exec for all paths, got %d: %q", len(r.argv), r.argv)
	}
	a := r.argv[0]
	if len(a) != 7 || !slices.Equal(a[:6], []string{"ssh", "--profile", "agent-box", "--", "sh", "-c"}) {
		t.Fatalf("argv = %q, want ssh --profile agent-box -- sh -c <one script>", a)
	}
	if !slices.Equal(r.envs[0], v.env()) {
		t.Fatalf("ssh env = %v, want the pinned env", r.envs[0])
	}
	script := a[6]
	// No path byte reaches the shell: not even a space of a path does.
	for _, p := range awkwardPaths {
		if strings.Contains(script, p) || strings.Contains(script, strings.ReplaceAll(p, " ", "")) {
			t.Fatalf("script carries path %q in the clear:\n%s", p, script)
		}
	}
	words := listWords(t, script)
	if len(words) != len(awkwardPaths) {
		t.Fatalf("%d words for %d paths", len(words), len(awkwardPaths))
	}
	for i, w := range words {
		if !base64Word.MatchString(w) {
			t.Fatalf("word %q is not plain base64 (a shell could read it as syntax)", w)
		}
		got, err := base64.StdEncoding.DecodeString(w)
		if err != nil || string(got) != awkwardPaths[i] {
			t.Fatalf("word %d decodes to %q (%v), want %q", i, got, err, awkwardPaths[i])
		}
	}
}

func TestPathIDsSkipsAnEmptyList(t *testing.T) {
	r := &sshRunner{}
	v := vmWith(t, r)
	r.argv = nil
	ids, err := v.PathIDs(context.Background(), nil)
	if err != nil || len(ids) != 0 || len(r.argv) != 0 {
		t.Fatalf("no paths must cost no exec: ids=%v err=%v calls=%q", ids, err, r.argv)
	}
}

func TestPathIDsFailsClosed(t *testing.T) {
	two := []string{"/a", "/b"}
	for name, c := range map[string]struct {
		out string
		err error
	}{
		"exec error":         {out: "path 1: a component is a symlink\n", err: errors.New("colima ssh: exit status 1: path 1: a component is a symlink")},
		"no output":          {out: ""},
		"too few lines":      {out: "ctxid 44:393\n"},
		"too many lines":     {out: "ctxid 44:393\nctxid 44:394\nctxid 44:395\n"},
		"not a number":       {out: "ctxid 44:393\nctxid 44:abc\n"},
		"empty field":        {out: "ctxid 44:393\nctxid :5\n"},
		"overflows uint64":   {out: "ctxid 44:393\nctxid 44:99999999999999999999\n"},
		"no marker":          {out: "44:393\n44:394\n"},
		"marker mid-line":    {out: "ctxid 44:393\nnoise ctxid 44:394\n"},
		"a path in a header": {out: "ctxid 44:393 /b\nctxid 44:394\n"},
	} {
		t.Run(name, func(t *testing.T) {
			v := vmWith(t, &sshRunner{out: c.out, err: c.err})
			ids, err := v.PathIDs(context.Background(), two)
			if err == nil || ids != nil {
				t.Fatalf("must fail closed: ids=%v err=%v", ids, err)
			}
		})
	}
}

func TestPathIDsIgnoresNoiseAroundTheIdentityLines(t *testing.T) {
	out := "WARN[0000] lima says something\nctxid 44:393\nctxid 16777232:153593034\nShared connection closed.\n"
	v := vmWith(t, &sshRunner{out: out})
	ids, err := v.PathIDs(context.Background(), []string{"/a", "/b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []FileID{{44, 393}, {16777232, 153593034}}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %+v, want %+v", ids, want)
	}
}

func TestPathIDsRefusesWhenTheConfigCannotRun(t *testing.T) {
	r := &sshRunner{out: "ctxid 1:2\n"}
	v := NewVM(Config{}, r) // an invalid config: preflight refuses before any exec
	if _, err := v.PathIDs(context.Background(), []string{"/a"}); err == nil {
		t.Fatal("an unusable VM config must refuse")
	}
	if len(r.argv) != 0 {
		t.Fatalf("no exec expected, got %q", r.argv)
	}
}

// vmWith is a provisioned VM whose runner is r from here on: provisioning
// itself needs fakeRunner's colima answers, the code under test needs r's.
func vmWith(t *testing.T, r Runner) *VM {
	t.Helper()
	v := setUpVM(t, &fakeRunner{})
	v.r = r
	return v
}
