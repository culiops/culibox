package upgradebackup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/culibox/internal/account"
)

func writeMode(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// files lists every non-directory path under dir (relative, sorted) with its
// mode and contents.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m[rel] = info.Mode().String() + " " + string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// logRecorder collects Logf lines.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestCopiesAllowlistByteIdentical(t *testing.T) {
	r := t.TempDir()
	want := map[string]string{}
	add := func(rel, body string, mode os.FileMode) {
		writeMode(t, r, rel, body, mode)
		want[rel] = mode.String() + " " + body
	}
	skip := func(rel, body string) { writeMode(t, r, rel, body, 0o600) }

	for i, n := range topFiles {
		add(n, "top "+n+fmt.Sprint(i), 0o600)
	}
	add("config.json", `{"c":1}`, 0o600)
	add("store/recipient", "age1xyz", 0o600)
	add("store/identity.age", "\x00\x01cipher\xff", 0o600)
	for _, v := range []string{"a", "b", "c"} {
		add("store/values/"+v+".age", "val-"+v, 0o600)
	}
	add("clone-bases/x/y", "cb", 0o644)
	add("clone-imports/i", "ci", 0o600)
	add("clone-return/r", "cr", 0o600)
	add("bin/agent-box-statusline.sh", "#!/bin/sh\n", 0o755)
	for _, acct := range []string{"work", "home"} {
		base := "accounts/" + acct + "/claude/"
		for _, f := range accountFiles {
			add(base+f, "acct "+acct+f, 0o600)
		}
		skip(base+".credentials.json", "SECRET-OAUTH")
		skip(base+"projects/x.jsonl", "transcript")
	}
	skip("boxstate/b/f", "big")
	skip("analytics.db", "derived")
	skip("bundle/x", "b")
	skip("vm/x", "v")
	skip("serve.lock", "l")
	writeFile(t, r, StampName, "v0 old\n")
	want[StampName] = "-rw------- v0 old\n"

	o := opts(r, "v1")
	o.IncludeConfig = true
	res, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken {
		t.Fatalf("no backup: %+v", res)
	}
	got := files(t, res.Dir)
	if !reflect.DeepEqual(got, want) {
		var gk, wk []string
		for k := range got {
			gk = append(gk, k)
		}
		for k := range want {
			wk = append(wk, k)
		}
		sort.Strings(gk)
		sort.Strings(wk)
		t.Fatalf("backup mismatch\n got keys %v\nwant keys %v", gk, wk)
	}
	b, _ := os.ReadFile(filepath.Join(r, StampName))
	if string(b) != "v1 abc\n" {
		t.Fatalf("stamp = %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(r, "backups", ".tmp-*")); len(left) != 0 {
		t.Fatalf("tmp dir left: %v", left)
	}
}

func TestConfigOnlyWhenInRoot(t *testing.T) {
	for _, include := range []bool{false, true} {
		r := t.TempDir()
		writeFile(t, r, "profiles.json", "{}")
		writeFile(t, r, "config.json", `{"x":1}`)
		o := opts(r, "v1")
		o.IncludeConfig = include
		res, err := Run(o)
		if err != nil || !res.Taken {
			t.Fatalf("include=%v: %+v, %v", include, res, err)
		}
		_, err = os.Lstat(filepath.Join(res.Dir, "config.json"))
		if include && err != nil {
			t.Fatalf("config.json missing with IncludeConfig=true: %v", err)
		}
		if !include && !os.IsNotExist(err) {
			t.Fatalf("config.json present with IncludeConfig=false: %v", err)
		}
	}
}

func TestSymlinkNotFollowed(t *testing.T) {
	out := t.TempDir()
	writeFile(t, out, "secret.txt", "OUTSIDE-PROFILE")
	writeFile(t, out, "stolen", "OUTSIDE-STORE")
	if err := os.Mkdir(filepath.Join(out, "sdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMode(t, out, "sdir/k.age", "OUTSIDE-STORE", 0o600)

	r := t.TempDir()
	writeFile(t, r, "tokens.json", "{}")
	if err := os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(r, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "sdir"), filepath.Join(r, "store")); err != nil {
		t.Fatal(err)
	}
	// A symlink nested inside an included dir is skipped too.
	writeMode(t, r, "clone-bases/ok", "ok", 0o600)
	if err := os.Symlink(filepath.Join(out, "stolen"), filepath.Join(r, "clone-bases", "ln")); err != nil {
		t.Fatal(err)
	}
	var lr logRecorder
	o := opts(r, "v1")
	o.Logf = lr.logf
	res, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	got := files(t, res.Dir)
	for p, v := range got {
		if strings.Contains(v, "OUTSIDE") {
			t.Fatalf("followed a symlink: %s = %q", p, v)
		}
	}
	if _, ok := got["tokens.json"]; !ok {
		t.Fatalf("tokens.json missing: %v", got)
	}
	if _, ok := got["clone-bases/ok"]; !ok {
		t.Fatalf("clone-bases/ok missing: %v", got)
	}
	logs := lr.joined()
	for _, want := range []string{"profiles.json", "store", filepath.Join("clone-bases", "ln")} {
		if !strings.Contains(logs, want) {
			t.Fatalf("Logf did not name %q:\n%s", want, logs)
		}
	}
}

func TestUnreadableFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a chmod-000 file")
	}
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	writeMode(t, r, "tokens.json", "{}", 0o600)
	writeFile(t, r, StampName, "v0 old\n")
	if err := os.Chmod(filepath.Join(r, "tokens.json"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(r, "tokens.json"), 0o600) })

	res, err := Run(opts(r, "v1"))
	if err == nil {
		t.Fatalf("no error: %+v", res)
	}
	if !strings.Contains(err.Error(), "tokens.json") {
		t.Fatalf("error does not name the file: %v", err)
	}
	if res.Taken {
		t.Fatalf("Taken with a failed backup: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(r, StampName))
	if string(b) != "v0 old\n" {
		t.Fatalf("stamp changed to %q after a failed backup", b)
	}
	ents, _ := os.ReadDir(filepath.Join(r, "backups"))
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("finished backup dir %q after a failure", e.Name())
		}
	}
}

func TestLeftoverTmpCleaned(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	writeMode(t, r, "backups/.tmp-old/x", "junk", 0o600)
	writeMode(t, r, "backups/.tmp-old/sub/y", "junk", 0o600)
	keep := "backups/some-other-dir/z"
	writeMode(t, r, keep, "keep", 0o600)
	res, err := Run(opts(r, "v1"))
	if err != nil || !res.Taken {
		t.Fatalf("%+v, %v", res, err)
	}
	if _, err := os.Lstat(filepath.Join(r, "backups", ".tmp-old")); !os.IsNotExist(err) {
		t.Fatalf(".tmp-old survived: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(r, keep)); err != nil {
		t.Fatalf("unrelated dir touched: %v", err)
	}
}

func TestRerunAfterStampLoss(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	writeFile(t, r, StampName, "v0 old\n")
	res1, err := Run(opts(r, "v1"))
	if err != nil || !res1.Taken {
		t.Fatalf("first: %+v, %v", res1, err)
	}
	writeFile(t, r, StampName, "v0 old\n") // crash before the stamp: old stamp again
	res2, err := Run(opts(r, "v1"))
	if err != nil || !res2.Taken {
		t.Fatalf("second: %+v, %v", res2, err)
	}
	if res1.Dir == res2.Dir {
		t.Fatalf("both runs used %s", res1.Dir)
	}
	ents, _ := os.ReadDir(filepath.Join(r, "backups"))
	if len(ents) != 2 {
		t.Fatalf("backups = %v, want 2", ents)
	}
}

func TestDirCollisionSuffix(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "first")
	var dirs []string
	for i := 0; i < 3; i++ {
		writeFile(t, r, "profiles.json", fmt.Sprintf("body-%d", i))
		writeFile(t, r, StampName, "v0 old\n")
		res, err := Run(opts(r, "v1")) // same Now every time
		if err != nil || !res.Taken {
			t.Fatalf("run %d: %+v, %v", i, res, err)
		}
		dirs = append(dirs, res.Dir)
	}
	base := []string{
		"v0-to-v1-20261113T103000Z",
		"v0-to-v1_2-20261113T103000Z",
		"v0-to-v1_3-20261113T103000Z",
	}
	re := regexp.MustCompile(`-\d{8}T\d{6}Z$`)
	for i, d := range dirs {
		if filepath.Base(d) != base[i] {
			t.Fatalf("dir %d = %s, want %s", i, filepath.Base(d), base[i])
		}
		if !re.MatchString(d) {
			t.Fatalf("timestamp not trailing: %s", d)
		}
		b, err := os.ReadFile(filepath.Join(d, "profiles.json"))
		if err != nil || string(b) != fmt.Sprintf("body-%d", i) {
			t.Fatalf("dir %d profiles.json = %q, %v (an earlier backup was overwritten?)", i, b, err)
		}
	}
}

func TestAccountFileNamesMatchAccountPackage(t *testing.T) {
	have := map[string]bool{}
	for _, f := range accountFiles {
		have[f] = true
	}
	for _, want := range []string{account.MCPFileName, account.DelegateFileName, "settings.json", ".agentbox-manifest.json"} {
		if !have[want] {
			t.Errorf("accountFiles lacks %q: %v", want, accountFiles)
		}
	}
	if len(accountFiles) != 4 {
		t.Errorf("accountFiles = %v, want exactly 4", accountFiles)
	}
}

func TestFreshInstallAnyIncludedItemCounts(t *testing.T) {
	cases := map[string]string{
		"store":        "store/values/a.age",
		"clone-bases":  "clone-bases/x",
		"clone-return": "clone-return/y/z",
		"bin":          "bin/statusline.sh",
		"account":      "accounts/n/claude/settings.json",
		"audit":        "audit.db",
	}
	for name, rel := range cases {
		r := t.TempDir()
		writeFile(t, r, "keep", "") // R is not empty
		writeMode(t, r, rel, "x", 0o600)
		res, err := Run(opts(r, "v1"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !res.Taken {
			t.Fatalf("%s: treated as a fresh install: %+v", name, res)
		}
		if name == "audit" {
			continue // Task 3 snapshots audit.db; here it only has to count as data
		}
		if _, err := os.Lstat(filepath.Join(res.Dir, rel)); err != nil {
			t.Fatalf("%s: %s not in the backup: %v", name, rel, err)
		}
	}
}

func TestFreshInstallIgnoresEmptyDirsAndExcluded(t *testing.T) {
	r := t.TempDir()
	for _, d := range []string{"store", "clone-bases", "accounts/n/claude", "bin"} {
		if err := os.MkdirAll(filepath.Join(r, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeMode(t, r, "boxstate/b/f", "x", 0o600)
	writeMode(t, r, "accounts/n/claude/.credentials.json", "x", 0o600)
	res, err := Run(opts(r, "v1"))
	if err != nil || res.Taken {
		t.Fatalf("%+v, %v", res, err)
	}
}
