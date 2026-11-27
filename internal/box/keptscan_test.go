package box

import (
	"context"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// keptHome is a fake box home: a temp dir and an os.Root on it.
func keptHome(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return dir, root
}

func keptWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keptMkdir(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func keptSymlink(t *testing.T, target, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

func candRels(cs []keptCandidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Rel)
	}
	return out
}

// watchOpens records every rel openDirIn and the screen open, for the test.
func watchOpens(t *testing.T) func() []string {
	t.Helper()
	var seen []string
	openDirHook = func(rel string) { seen = append(seen, rel) }
	keptOpenHook = func(rel string) { seen = append(seen, rel) }
	t.Cleanup(func() { openDirHook, keptOpenHook = nil, nil })
	return func() []string { return seen }
}

func mustCandidates(t *testing.T, home *os.Root, mounts []string) ([]keptCandidate, []keptRefusal) {
	t.Helper()
	cs, rs, err := keptCandidates(home, mounts)
	if err != nil {
		t.Fatalf("keptCandidates: %v", err)
	}
	return cs, rs
}

func TestCandidatesLevels(t *testing.T) {
	dir, home := keptHome(t)
	keptWrite(t, dir, ".culiops/a", "a")
	keptWrite(t, dir, ".config/culiops/b", "b")
	keptWrite(t, dir, ".local/share/x/c", "c")
	keptWrite(t, dir, ".local/state/y/d", "d")
	keptWrite(t, dir, ".bashrc", "export X=1")
	keptMkdir(t, dir, ".empty")
	keptWrite(t, dir, "notes/e", "not a dot dir") // only ~/.<name>/ is a level
	keptWrite(t, dir, ".local/bin/tool", "")      // .local/bin is not a level
	if err := unix.Mkfifo(filepath.Join(dir, ".pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := watchOpens(t)

	cs, rs := mustCandidates(t, home, nil)
	want := []string{".config/culiops", ".culiops", ".local/share/x", ".local/state/y"}
	if got := candRels(cs); !slices.Equal(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
	if len(rs) != 0 {
		t.Fatalf("refusals = %+v, want none", rs)
	}
	for _, c := range cs {
		fi, err := os.Lstat(filepath.Join(dir, c.Rel))
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if c.Dev != uint64(st.Dev) || c.Ino != uint64(st.Ino) {
			t.Fatalf("%s: dev/ino = %d/%d, want %d/%d", c.Rel, c.Dev, c.Ino, st.Dev, st.Ino)
		}
	}
	if slices.Contains(opened(), ".pipe") {
		t.Fatal("a FIFO at the top of the home was opened")
	}
}

func TestCandidatesSkips(t *testing.T) {
	dir, home := keptHome(t)
	for _, rel := range []string{".cache", ".npm", "go", ".terraform.d", ".claude", ".agent-box",
		".cargo", ".vscode-server", ".local/share/pnpm", ".local/share/virtualenvs"} {
		keptWrite(t, dir, rel+"/f", "x")
	}
	keptWrite(t, dir, ".tagged/CACHEDIR.TAG", "Signature: 8a477f597d28d172789f06886806bc55\n")
	keptWrite(t, dir, ".tagged/blob", "x")
	keptWrite(t, dir, ".config/tagged/CACHEDIR.TAG", "Signature: 8a477f597d28d172789f06886806bc55\n")
	keptWrite(t, dir, ".config/tagged/blob", "x")
	// Letter case is ignored: on macOS .Claude is CuliBox's own .claude.
	keptWrite(t, dir, ".Claude/f", "x")

	cs, rs := mustCandidates(t, home, nil)
	if len(cs) != 0 || len(rs) != 0 {
		t.Fatalf("candidates = %q, refusals = %+v, want none of either", candRels(cs), rs)
	}
}

func TestCandidatesCredentialNames(t *testing.T) {
	dir, home := keptHome(t)
	creds := []string{".aws", ".ssh", ".config/gh", ".mozilla", ".local/share/keyrings", ".config/gcloud"}
	for _, rel := range creds {
		keptWrite(t, dir, rel+"/f", "x")
	}
	keptWrite(t, dir, ".AWS2/f", "x") // not a credential name
	cs, rs := mustCandidates(t, home, nil)
	if got := candRels(cs); !slices.Equal(got, []string{".AWS2"}) {
		t.Fatalf("candidates = %q, want only .AWS2", got)
	}
	got := map[string]string{}
	for _, r := range rs {
		got[r.Rel] = r.Reason
	}
	for _, rel := range creds {
		if got[rel] != "credentials" {
			t.Errorf("%s: reason %q, want credentials", rel, got[rel])
		}
	}
	if len(rs) != len(creds) {
		t.Fatalf("refusals = %+v, want one per credential dir", rs)
	}
}

func TestCandidatesCredentialEmptyIsSilent(t *testing.T) {
	dir, home := keptHome(t)
	keptMkdir(t, dir, ".ssh") // made by ssh, nothing in it: no row each pass
	cs, rs := mustCandidates(t, home, nil)
	if len(cs) != 0 || len(rs) != 0 {
		t.Fatalf("candidates = %q, refusals = %+v, want none", candRels(cs), rs)
	}
}

func TestCandidatesRootSymlink(t *testing.T) {
	dir, home := keptHome(t)
	keptSymlink(t, "/etc", dir, ".foo")
	keptSymlink(t, "/etc", dir, ".config/bar")
	keptSymlink(t, "/etc", dir, ".aws") // a link with a credential name says credentials
	keptWrite(t, dir, ".real/x", "x")
	keptSymlink(t, ".real", dir, ".inroot") // a link that stays in the home is a link too
	opened := watchOpens(t)

	cs, rs := mustCandidates(t, home, nil)
	if got := candRels(cs); !slices.Equal(got, []string{".real"}) {
		t.Fatalf("candidates = %q, want only .real", got)
	}
	want := []keptRefusal{
		{Rel: ".aws", Reason: "credentials"},
		{Rel: ".config/bar", Reason: "is a link"},
		{Rel: ".foo", Reason: "is a link"},
		{Rel: ".inroot", Reason: "is a link"},
	}
	if !slices.Equal(rs, want) {
		t.Fatalf("refusals = %+v, want %+v", rs, want)
	}
	for _, rel := range opened() {
		if rel == ".foo" || rel == ".config/bar" || rel == ".aws" || rel == ".inroot" {
			t.Fatalf("a link at a candidate root was opened: %q", rel)
		}
	}
}

func TestCandidatesLevelSymlink(t *testing.T) {
	dir, home := keptHome(t)
	keptSymlink(t, "/etc", dir, ".config")
	keptWrite(t, dir, ".elsewhere/share/app/f", "x")
	keptMkdir(t, dir, ".local")
	keptSymlink(t, "../.elsewhere/share", dir, ".local/share")
	opened := watchOpens(t)

	cs, rs := mustCandidates(t, home, nil)
	if got := candRels(cs); !slices.Equal(got, []string{".elsewhere"}) {
		t.Fatalf("candidates = %q, want only .elsewhere", got)
	}
	want := []keptRefusal{{Rel: ".config", Reason: "is a link"}, {Rel: ".local/share", Reason: "is a link"}}
	if !slices.Equal(rs, want) {
		t.Fatalf("refusals = %+v, want %+v", rs, want)
	}
	for _, rel := range opened() {
		if strings.HasPrefix(rel, ".config") || strings.HasPrefix(rel, ".local/share") {
			t.Fatalf("a linked level was opened: %q", rel)
		}
	}
}

func TestCandidatesHoldsCuliBoxMount(t *testing.T) {
	dir, home := keptHome(t)
	keptWrite(t, dir, ".culiops/operator.md", "") // the mountpoint stub
	keptWrite(t, dir, ".culiops/notes.md", "mine")
	keptWrite(t, dir, ".config/app/inc/x.md", "")
	keptWrite(t, dir, ".config/app/y", "y")
	keptWrite(t, dir, ".mounted/z", "the mountpoint itself is CuliBox's own")
	keptWrite(t, dir, ".config/under/z", "z")

	mounts := []string{
		".culiops/operator.md",
		boxHome + "/.config/app/inc/x.md", // an in-box path is read as its home-relative one
		".mounted",
		"/opt/agent-bundle", // outside the home: ignored
	}
	cs, rs := mustCandidates(t, home, mounts)
	if got := candRels(cs); !slices.Equal(got, []string{".config/under"}) {
		t.Fatalf("candidates = %q, want only .config/under", got)
	}
	want := []keptRefusal{
		{Rel: ".config/app", Reason: "holds a file CuliBox mounts from the host"},
		{Rel: ".culiops", Reason: "holds a file CuliBox mounts from the host"},
	}
	if !slices.Equal(rs, want) {
		t.Fatalf("refusals = %+v, want %+v", rs, want)
	}

	// A mount covering a level hides everything below it from the box.
	cs, rs = mustCandidates(t, home, []string{".config"})
	if got := candRels(cs); !slices.Equal(got, []string{".culiops", ".mounted"}) || len(rs) != 0 {
		t.Fatalf("with .config mounted: candidates = %q, refusals = %+v", got, rs)
	}
}

func TestCandidatesNameNotAllowed(t *testing.T) {
	dir, home := keptHome(t)
	bad := []string{".a,b", ".a:b", `.a"b`, ".a\tb", ".config/a,b", ".trail ", ".config/dot."}
	for _, rel := range bad {
		keptWrite(t, dir, rel+"/f", "x")
	}
	cs, rs := mustCandidates(t, home, nil)
	if len(cs) != 0 {
		t.Fatalf("candidates = %q, want none", candRels(cs))
	}
	got := map[string]string{}
	for _, r := range rs {
		got[r.Rel] = r.Reason
	}
	for _, rel := range bad {
		if got[rel] != "name not allowed" {
			t.Errorf("%q: reason %q, want name not allowed", rel, got[rel])
		}
	}
}

func TestKeptNameOK(t *testing.T) {
	for rel, want := range map[string]bool{
		".culiops":            true,
		".config/culiops":     true,
		".local/share/x y":    true,
		"":                    false,
		".a,b":                false,
		".a:b":                false,
		`.a"b`:                false,
		".a\nb":               false,
		".a\x7fb":             false,
		".a\u0085b":           false,
		".a\u2028b":           false,
		".config/a:b":         false,
		".bad\xffutf8":        false,
		".local/share/ok-1.2": true,
		".a b":                true,
		".a ":                 false, // a trailing space or dot: docker and the VM can read it another way
		".a.":                 false,
		".config/a ":          false,
		".local/share/x.":     false,
	} {
		if got := keptNameOK(rel); got != want {
			t.Errorf("keptNameOK(%q) = %v, want %v", rel, got, want)
		}
	}
}

// TestOwnPathsCoverSeeded pins keptOwn to what CuliBox seeds in a home: a new
// seeded directory that is not listed there would show up as a candidate.
func TestOwnPathsCoverSeeded(t *testing.T) {
	m := newTestManager(t)
	const name = "ab-box-keptown"
	src := t.TempDir()
	keptWrite(t, src, "CLAUDE.md", "x")
	keptMkdir(t, src, "skills")
	keptMkdir(t, src, "plugin")
	mounts := []Mount{
		{Host: filepath.Join(src, "skills"), Box: boxHome + "/.claude/skills"},
		{Host: filepath.Join(src, "CLAUDE.md"), Box: boxHome + "/.claude/CLAUDE.md"},
		{Host: filepath.Join(src, "plugin"), Box: boxHome + "/.claude/plugins/p"},
	}
	if _, err := m.ensureStateDir(name, "/w", mounts); err != nil {
		t.Fatal(err)
	}
	convDir, err := ensureConversationsDir(m.opts.StateRoot, ConversationsKey("TEST", "/w", "", "prof"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ensureConversationsMount(name, convDir, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.preAcceptBypass(name); err != nil {
		t.Fatal(err)
	}
	m.prepareHomePid(name, "sess-1")
	if _, err := os.Lstat(filepath.Join(m.stateDir(name), HomePidDir)); err != nil {
		t.Fatalf("the pid dir was not seeded: %v", err)
	}

	home, err := os.OpenRoot(m.stateDir(name))
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	var cb []string
	for _, mt := range mounts {
		cb = append(cb, strings.TrimPrefix(mt.Box, boxHome+"/"))
	}
	cs, rs := mustCandidates(t, home, cb)
	if len(cs) != 0 || len(rs) != 0 {
		t.Fatalf("a seeded home gave candidates %q and refusals %+v: list the new path in keptOwn", candRels(cs), rs)
	}
}

func screen(t *testing.T, root *os.Root, rel string) screenResult {
	t.Helper()
	return screenKept(context.Background(), root, rel)
}

func TestScreenNameRules(t *testing.T) {
	for _, tc := range []struct {
		file, content, reason string
	}{
		{"id_rsa", "not even a key", "looks like credentials: .culiops/id_rsa"},
		{"sub/.env", "X=1", "looks like credentials: .culiops/sub/.env"},
		{"credentials", "", "looks like credentials: .culiops/credentials"},
		{"terraform.tfstate", "{}", "looks like credentials: .culiops/terraform.tfstate"},
		{"config.json", `{"theme":"dark"}`, ""},
		{"config.prod.json", `{"theme":"dark"}`, ""},
		{"config.json", `{"token":"ghp_` + strings.Repeat("a", 36) + `"}`, "looks like credentials: .culiops/config.json"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir, root := keptHome(t)
			keptWrite(t, dir, ".culiops/"+tc.file, tc.content)
			keptWrite(t, dir, ".culiops/notes.md", "plain notes")
			if got := screen(t, root, ".culiops"); got.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.reason)
			}
		})
	}
}

func TestScreenContent(t *testing.T) {
	cloudTrail := `{"Records":[{"eventVersion":"1.08","userIdentity":{"type":"AssumedRole",` +
		`"principalId":"AROAXXXXXXXXXXXXXXXXX:alice","arn":"arn:aws:sts::123456789012:assumed-role/ops/alice",` +
		`"accountId":"123456789012","accessKeyId":"ASIAQWERTYUIOPASDFGH","sessionContext":{"attributes":` +
		`{"creationDate":"2026-11-27T01:02:03Z","mfaAuthenticated":"false"}}},"eventTime":"2026-11-27T01:02:04Z",` +
		`"eventSource":"ec2.amazonaws.com","eventName":"DescribeInstances","awsRegion":"ap-southeast-1",` +
		`"sourceIPAddress":"203.0.113.9","userAgent":"aws-cli/2.17","requestParameters":null,` +
		`"responseElements":null,"requestID":"0b1c","eventID":"9f2e","readOnly":true}, ` +
		`{"userIdentity":{"accessKeyId":"AKIAQWERTYUIOPASDFGH"}}]}`
	for _, tc := range []struct {
		name, content, reason string
	}{
		{"pem", "notes\n-----BEGIN RSA PRIVATE KEY-----\nMIIE\n", "looks like credentials: .culiops/data.txt"},
		{"aws secret", "[default]\naws_secret_access_key = abc/def\n", "looks like credentials: .culiops/data.txt"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9xx.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig", "looks like credentials: .culiops/data.txt"},
		{"cloudtrail ids", cloudTrail, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, root := keptHome(t)
			keptWrite(t, dir, ".culiops/data.txt", tc.content)
			if got := screen(t, root, ".culiops"); got.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.reason)
			}
		})
	}
}

func TestScreenHeadOnly(t *testing.T) {
	dir, root := keptHome(t)
	content := strings.Repeat("x", keptScreenHead) + "\n-----BEGIN RSA PRIVATE KEY-----\n"
	keptWrite(t, dir, ".culiops/big", content)
	got := screen(t, root, ".culiops")
	if got.Reason != "" {
		t.Fatalf("reason = %q: only the first %d bytes are read", got.Reason, keptScreenHead)
	}
	if got.Bytes != int64(len(content)) {
		t.Fatalf("bytes = %d, want the whole file's size", got.Bytes)
	}
}

func TestScreenCounts(t *testing.T) {
	dir, root := keptHome(t)
	keptWrite(t, dir, ".config/culiops/a", "12345")
	keptWrite(t, dir, ".config/culiops/sub/b", "123")
	keptSymlink(t, "a", dir, ".config/culiops/l")
	got := screen(t, root, ".config/culiops")
	if got.Reason != "" || got.Files != 3 || got.Bytes != 8 {
		t.Fatalf("got %+v, want a pass with 3 files (two plus a link) and 8 bytes", got)
	}
}

func setCap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestScreenCapEntries(t *testing.T) {
	setCap(t, &keptScreenMaxEntries, 10)
	dir, root := keptHome(t)
	for i := range 10 {
		keptWrite(t, dir, ".culiops/f"+strconv.Itoa(i), "x")
	}
	if got := screen(t, root, ".culiops"); got.Reason != "" {
		t.Fatalf("10 files: reason = %q, want a pass", got.Reason)
	}
	keptWrite(t, dir, ".culiops/f10", "x")
	if got := screen(t, root, ".culiops"); got.Reason != "too many files to check" {
		t.Fatalf("11 files: reason = %q, want too many files to check", got.Reason)
	}
	// Directories count as entries too.
	dir2, root2 := keptHome(t)
	keptMkdir(t, dir2, ".culiops/1/2/3/4/5/6/7/8/9/10/11")
	setCap(t, &keptScreenMaxDepth, 20)
	if got := screen(t, root2, ".culiops"); got.Reason != "too many files to check" {
		t.Fatalf("11 nested dirs: reason = %q, want too many files to check", got.Reason)
	}
}

// A folder of data files (the spec's own example: Parquet data) is screened
// within the time cap: 300 files of 64 KiB of random bytes each.
func TestScreenDataFolderInTime(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 19 MiB")
	}
	dir, root := keptHome(t)
	r := mathrand.New(mathrand.NewSource(3))
	b := make([]byte, 64<<10)
	for i := range 300 {
		r.Read(b)
		keptWrite(t, dir, ".local/share/culiops/data/part-"+strconv.Itoa(i)+".parquet", string(b))
	}
	start := time.Now()
	got := screen(t, root, ".local/share/culiops")
	if got.Reason != "" || got.Files != 300 {
		t.Fatalf("after %v: %+v, want a pass over 300 files", time.Since(start), got)
	}
	t.Logf("300 files of 64 KiB screened in %v", time.Since(start))
}

func TestScreenDepth(t *testing.T) {
	dir, root := keptHome(t)
	keptWrite(t, dir, ".culiops/1/2/3/4/5/6/7/f", "x") // f is at depth 8
	if got := screen(t, root, ".culiops"); got.Reason != "" {
		t.Fatalf("depth 8: reason = %q, want a pass", got.Reason)
	}
	keptWrite(t, dir, ".culiops/1/2/3/4/5/6/7/8/f", "x") // depth 9
	if got := screen(t, root, ".culiops"); got.Reason != "too many files to check" {
		t.Fatalf("depth 9: reason = %q, want too many files to check", got.Reason)
	}
}

func TestScreenTimeout(t *testing.T) {
	dir, root := keptHome(t)
	keptWrite(t, dir, ".culiops/a", "x")
	setCap(t, &keptScreenTimeout, time.Duration(0))
	if got := screen(t, root, ".culiops"); got.Reason != "too slow to check" || !got.Timeout {
		t.Fatalf("past the deadline: %+v, want too slow to check, on the time cap", got)
	}
	setCap(t, &keptScreenTimeout, time.Minute)
	setCap(t, &keptScreenMaxEntries, 0)
	if got := screen(t, root, ".culiops"); got.Reason != "too many files to check" || got.Timeout {
		t.Fatalf("entry cap: %+v, want too many files to check, not on the time cap", got)
	}
	keptScreenMaxEntries = 20000
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := screenKept(ctx, root, ".culiops"); got.Reason != "could not read" || got.Timeout {
		t.Fatalf("cancelled: %+v, want could not read", got)
	}
	// The caller's own deadline is not the screen's cap.
	past, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	if got := screenKept(past, root, ".culiops"); got.Reason != "could not read" {
		t.Fatalf("caller's deadline: reason = %q, want could not read", got.Reason)
	}
}

func TestScreenInnerSymlinkKeptNotFollowed(t *testing.T) {
	dir, root := keptHome(t)
	outside := t.TempDir()
	keptWrite(t, outside, "id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	keptWrite(t, dir, ".ssh/key", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	keptWrite(t, dir, ".culiops/notes.md", "plain")
	keptSymlink(t, "/etc/passwd", dir, ".culiops/passwd")
	keptSymlink(t, filepath.Join(outside, "id_rsa"), dir, ".culiops/out")
	keptSymlink(t, "../.ssh/key", dir, ".culiops/in") // stays in the root: os.Root would follow it
	keptSymlink(t, "../.ssh", dir, ".culiops/sshdir") // a link to a directory is not walked
	opened := watchOpens(t)

	got := screen(t, root, ".culiops")
	if got.Reason != "" {
		t.Fatalf("reason = %q: an inner link is kept as it is, never followed", got.Reason)
	}
	if got.Files != 5 {
		t.Fatalf("files = %d, want 5 (one file and four links)", got.Files)
	}
	for _, rel := range opened() {
		if strings.HasPrefix(rel, ".culiops/") && rel != ".culiops/notes.md" {
			t.Fatalf("the screen opened %q", rel)
		}
	}
}

func TestScreenFIFO(t *testing.T) {
	dir, root := keptHome(t)
	keptWrite(t, dir, ".culiops/a", "x")
	if err := unix.Mkfifo(filepath.Join(dir, ".culiops", "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := watchOpens(t)
	done := make(chan screenResult, 1)
	go func() { done <- screen(t, root, ".culiops") }()
	select {
	case got := <-done:
		if got.Reason != "could not read" {
			t.Fatalf("reason = %q, want could not read", got.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the screen blocked on a FIFO")
	}
	if slices.Contains(opened(), ".culiops/pipe") {
		t.Fatal("the screen opened the FIFO")
	}
}

func TestScreenRootNotPlain(t *testing.T) {
	dir, root := keptHome(t)
	keptWrite(t, dir, ".real/x", "x")
	keptSymlink(t, ".real", dir, ".link")
	keptSymlink(t, ".real", dir, ".configlink")
	keptWrite(t, dir, ".file", "x")
	for rel, want := range map[string]string{
		".link":            "is a link",
		".configlink/x":    "is a link", // a link on the way there
		".file":            "could not read",
		".missing":         "could not read",
		"../x":             "could not read",
		"/etc":             "could not read",
		".":                "could not read",
		".real":            "",
		".real/../.real":   "could not read", // not clean
		".config/../.real": "could not read",
	} {
		if got := screen(t, root, rel); got.Reason != want {
			t.Errorf("screenKept(%q) = %q, want %q", rel, got.Reason, want)
		}
	}
}

func keptDevIno(t *testing.T, p string) (uint64, uint64) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return uint64(st.Dev), uint64(st.Ino)
}

// swapOnOpen runs swap once, right before the first open of a name for which
// match holds, through either open hook.
func swapOnOpen(t *testing.T, match func(rel string) bool, swap func()) *bool {
	t.Helper()
	done := new(bool)
	hook := func(rel string) {
		if !*done && match(rel) {
			*done = true
			swap()
		}
	}
	openDirHook, keptOpenHook = hook, hook
	t.Cleanup(func() { openDirHook, keptOpenHook = nil, nil })
	return done
}

// A level swapped for a link to the home after it was listed must not make a
// child name CuliBox's own folder: every child is read from the level's own
// descriptor, and a level that no longer leads to what was listed yields no
// candidate.
func TestCandidatesSwappedLevelNotFollowed(t *testing.T) {
	dir, home := keptHome(t)
	keptWrite(t, dir, ".claude/settings.json", "{}")
	keptWrite(t, dir, ".agent-box/pids/s.pid", "1")
	keptWrite(t, dir, ".config/.claude/x", "the box's own folder of that name")
	keptWrite(t, dir, ".config/.agent-box/x", "the box's own folder of that name")
	var own [][2]uint64
	for _, rel := range []string{".claude", ".agent-box"} {
		d, i := keptDevIno(t, filepath.Join(dir, rel))
		own = append(own, [2]uint64{d, i})
	}
	swapped := swapOnOpen(t, func(rel string) bool { return strings.HasPrefix(rel, ".config/") }, func() {
		if err := os.Rename(filepath.Join(dir, ".config"), filepath.Join(dir, ".config-real")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(".", filepath.Join(dir, ".config")); err != nil {
			t.Fatal(err)
		}
	})

	cs, rs := mustCandidates(t, home, nil)
	if !*swapped {
		t.Fatal("the swap never ran")
	}
	for _, c := range cs {
		if slices.Contains(own, [2]uint64{c.Dev, c.Ino}) {
			t.Fatalf("candidate %q is CuliBox's own folder (dev/ino %d/%d)", c.Rel, c.Dev, c.Ino)
		}
		if strings.HasPrefix(c.Rel, ".config/") {
			t.Fatalf("candidate %q came from a level swapped while it was read", c.Rel)
		}
	}
	if !slices.Contains(rs, keptRefusal{Rel: ".config", Reason: "is a link"}) {
		t.Fatalf("refusals = %+v, want .config: is a link", rs)
	}
}

// The screen resolves its folder by descriptor too: a parent swapped for a link
// is never followed, and Dev/Ino say which folder was screened.
func TestScreenSwappedParentNotFollowed(t *testing.T) {
	for _, at := range []string{".config", ".config/app"} {
		t.Run(at, func(t *testing.T) {
			dir, root := keptHome(t)
			keptWrite(t, dir, ".config/app/notes.md", "plain")
			keptWrite(t, dir, "app/id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
			realDev, realIno := keptDevIno(t, filepath.Join(dir, ".config/app"))
			otherDev, otherIno := keptDevIno(t, filepath.Join(dir, "app"))
			swapped := swapOnOpen(t, func(rel string) bool { return rel == at }, func() {
				if err := os.Rename(filepath.Join(dir, ".config"), filepath.Join(dir, ".config-real")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(".", filepath.Join(dir, ".config")); err != nil {
					t.Fatal(err)
				}
			})
			got := screen(t, root, ".config/app")
			if !*swapped {
				t.Fatal("the swap never ran")
			}
			if got.Dev == otherDev && got.Ino == otherIno {
				t.Fatalf("the screen read ~/app through the swapped link: %+v", got)
			}
			if got.Reason == "" && (got.Dev != realDev || got.Ino != realIno) {
				t.Fatalf("a pass names dev/ino %d/%d, want the folder listed (%d/%d)", got.Dev, got.Ino, realDev, realIno)
			}
		})
	}
}

// What the screen reports as screened is the candidate discovery found.
func TestScreenDevInoMatchesCandidate(t *testing.T) {
	dir, home := keptHome(t)
	keptWrite(t, dir, ".culiops/a", "a")
	keptWrite(t, dir, ".config/culiops/b", "b")
	keptWrite(t, dir, ".local/share/x/c", "c")
	cs, _ := mustCandidates(t, home, nil)
	if len(cs) != 3 {
		t.Fatalf("candidates = %q", candRels(cs))
	}
	for _, c := range cs {
		got := screen(t, home, c.Rel)
		if got.Reason != "" || got.Dev != c.Dev || got.Ino != c.Ino {
			t.Fatalf("%s: screen %+v, candidate dev/ino %d/%d", c.Rel, got, c.Dev, c.Ino)
		}
	}
}

// TestImageOwnSkippedSilently: what the box image's entrypoint writes into the
// home at every start is neither a candidate nor a refusal row.
func TestImageOwnSkippedSilently(t *testing.T) {
	dir := t.TempDir()
	keptSymlink(t, "/usr/local/bin/terraform", dir, ".tfenv/versions/1.9.0/terraform")
	keptSymlink(t, "/opt/tflint/x", dir, ".tflint.d/plugins/x")
	keptSymlink(t, "/etc/agent-box/tflint.hcl", dir, ".tflint.hcl")
	keptWrite(t, dir, ".culiops/x.md", "x")
	home, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	cs, rs := mustCandidates(t, home, nil)
	if got := candRels(cs); !slices.Equal(got, []string{".culiops"}) || len(rs) != 0 {
		t.Fatalf("candidates %q refusals %+v, want only .culiops and no refusals", got, rs)
	}
	for _, n := range keptImageOwn {
		if validKeptRel(n) {
			t.Errorf("validKeptRel(%q) = true, a forged state entry could mount it", n)
		}
	}
}

// TestImageOwnPathsCoverEntrypoint pins keptImageOwn to the image: every top
// level home name the entrypoint writes must be skipped by the keep pass.
func TestImageOwnPathsCoverEntrypoint(t *testing.T) {
	b, err := os.ReadFile("../../docker/culibox/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	df := string(b)
	var ep string
	for _, l := range strings.Split(df, "\n") {
		if strings.HasPrefix(l, "RUN printf") && strings.Contains(l, "/usr/local/bin/agent-box-entrypoint") {
			ep = l
		}
	}
	if ep == "" {
		t.Fatal("entrypoint printf line not found in the Dockerfile")
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$HOME/(\.[A-Za-z0-9_.-]+)`).FindAllStringSubmatch(ep, -1) {
		names[m[1]] = true
	}
	envRe := regexp.MustCompile(`(?m)^ENV ([A-Z_]+)=/home/agent/(\.[A-Za-z0-9_-]+|[A-Za-z0-9_-]+)`)
	nEnv := 0
	for _, m := range envRe.FindAllStringSubmatch(df, -1) {
		if strings.Contains(ep, m[1]) {
			names[m[2]] = true
			nEnv++
		}
	}
	if !names[".tfenv"] || !names[".cache"] || nEnv < 2 {
		t.Fatalf("scan found %v: the Dockerfile layout changed, fix this test", names)
	}
	files := map[string]bool{".zshrc": true} // regular files are never candidates
	for n := range names {
		if files[n] || keptFoldIn(n, keptOwn) || keptFoldIn(n, keptCacheNames) || keptFoldIn(n, keptImageOwn) {
			continue
		}
		t.Errorf("the entrypoint writes ~/%s: add it to keptImageOwn", n)
	}
}
