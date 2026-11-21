package wsroots

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type auditCall struct{ action, path, actor string }

type rig struct {
	t       *testing.T
	dataDir string
	home    string
	calls   []auditCall
	auditEr error
	clock   time.Time
	svc     *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, clock: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	r.dataDir = realDir(t)
	r.home = realDir(t)
	r.svc = NewService(r.dataDir, r.home, func(action, path, actor string) error {
		r.calls = append(r.calls, auditCall{action, path, actor})
		return r.auditEr
	}, func() time.Time { return r.clock })
	return r
}

// realDir is a fresh temp dir with its symlinks resolved (macOS: /var -> /private/var).
func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (r *rig) file() string { return filepath.Join(r.dataDir, FileName) }

func TestCorruptFileNeverOverwritten(t *testing.T) {
	r := newRig(t)
	if err := os.WriteFile(r.file(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := realDir(t)
	if _, err := r.svc.List(); !errors.Is(err, ErrCorrupt) {
		t.Errorf("List err = %v, want ErrCorrupt", err)
	}
	if _, err := r.svc.Add(dir, "web"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Add err = %v, want ErrCorrupt", err)
	}
	if err := r.svc.Remove(dir, "web"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Remove err = %v, want ErrCorrupt", err)
	}
	r.svc.Touch(dir)
	if p := r.svc.Paths(); len(p) != 0 {
		t.Errorf("Paths = %v, want empty", p)
	}
	// Home still covers on a corrupt file ("home only").
	if got := r.svc.Covering(filepath.Join(r.home, "x")); got != r.home {
		t.Errorf("Covering under home = %q, want %q", got, r.home)
	}
	b, err := os.ReadFile(r.file())
	if err != nil || string(b) != "{" {
		t.Errorf("file = %q, %v; want untouched", b, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("audited on a corrupt file: %v", r.calls)
	}
}

func TestAddRefusesForbidden(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Add("/etc", "web")
	var re *RefusedError
	if !errors.As(err, &re) || re.Reason == "" {
		t.Fatalf("err = %v, want *RefusedError with a reason", err)
	}
	if _, serr := os.Stat(r.file()); serr == nil {
		t.Error("file written for a refused add")
	}
	if len(r.calls) != 0 {
		t.Errorf("audited a refused add: %v", r.calls)
	}
}

func TestAddRefusesLinkIntoForbidden(t *testing.T) {
	r := newRig(t)
	link := filepath.Join(realDir(t), "link")
	if err := os.Symlink(r.dataDir, link); err != nil {
		t.Fatal(err)
	}
	_, err := r.svc.Add(link, "web")
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RefusedError", err)
	}
}

func TestAddRefusesCredentialFolderInHome(t *testing.T) {
	r := newRig(t)
	ssh := filepath.Join(r.home, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	var re *RefusedError
	if _, err := r.svc.Add(ssh, "web"); !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RefusedError (refusal before covered)", err)
	}
}

func TestAddNotDir(t *testing.T) {
	r := newRig(t)
	dir := realDir(t)
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, filepath.Join(dir, "missing"), "relative/dir"} {
		if _, err := r.svc.Add(p, "web"); !errors.Is(err, ErrNotDir) {
			t.Errorf("Add(%q) err = %v, want ErrNotDir", p, err)
		}
	}
}

func TestAddCovered(t *testing.T) {
	r := newRig(t)
	// Home itself: ErrCovered, not the "contains credential folders" refusal (R1).
	if _, err := r.svc.Add(r.home, "web"); !errors.Is(err, ErrCovered) {
		t.Errorf("Add(home) err = %v, want ErrCovered", err)
	}
	sub := filepath.Join(r.home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(sub, "web"); !errors.Is(err, ErrCovered) {
		t.Errorf("Add(under home) err = %v, want ErrCovered", err)
	}
	// A home spelled through a symlink is resolved once.
	link := filepath.Join(realDir(t), "homelink")
	if err := os.Symlink(r.home, link); err != nil {
		t.Fatal(err)
	}
	svc := NewService(r.dataDir, link, nil, nil)
	if _, err := svc.Add(r.home, "web"); !errors.Is(err, ErrCovered) {
		t.Errorf("Add(real home) with a linked home err = %v, want ErrCovered", err)
	}

	root := realDir(t)
	if _, err := r.svc.Add(root, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(root, "web"); !errors.Is(err, ErrCovered) {
		t.Errorf("Add(same root) err = %v, want ErrCovered", err)
	}
	below := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(below, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(below, "web"); !errors.Is(err, ErrCovered) {
		t.Errorf("Add(under root) err = %v, want ErrCovered", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("audit calls = %v, want only the one real add", r.calls)
	}
}

func TestAddParentOfExistingRoot(t *testing.T) {
	r := newRig(t)
	parent := realDir(t)
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(child, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(parent, "web"); err != nil {
		t.Fatalf("adding a parent of a root: %v", err)
	}
	if got := r.svc.Paths(); !reflect.DeepEqual(got, []string{child, parent}) {
		t.Errorf("Paths = %v", got)
	}
	// Longest wins.
	if got := r.svc.Covering(filepath.Join(child, "x")); got != child {
		t.Errorf("Covering = %q, want %q", got, child)
	}
	if got := r.svc.Covering(filepath.Join(parent, "other")); got != parent {
		t.Errorf("Covering = %q, want %q", got, parent)
	}
	if got := r.svc.Covering(realDir(t)); got != "" {
		t.Errorf("Covering of an unrelated path = %q, want empty", got)
	}
	// Component-wise: a sibling sharing a name prefix is not covered.
	if got := r.svc.Covering(parent + "-sibling"); got != "" {
		t.Errorf("Covering(prefix sibling) = %q, want empty", got)
	}
}

func TestAddCap(t *testing.T) {
	r := newRig(t)
	parent := realDir(t)
	for i := 0; i < MaxRoots; i++ {
		d := filepath.Join(parent, string(rune('a'+i/26))+string(rune('a'+i%26)))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.Add(d, "web"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	extra := filepath.Join(parent, "extra")
	if err := os.Mkdir(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	n := len(r.calls)
	if _, err := r.svc.Add(extra, "web"); !errors.Is(err, ErrCap) {
		t.Fatalf("33rd add err = %v, want ErrCap", err)
	}
	if len(r.calls) != n {
		t.Error("audited a capped add")
	}
	if got := r.svc.Paths(); len(got) != MaxRoots {
		t.Errorf("%d roots, want %d", len(got), MaxRoots)
	}
}

func TestAuditFailureNoSave(t *testing.T) {
	r := newRig(t)
	r.auditEr = errors.New("audit down")
	dir := realDir(t)
	if _, err := r.svc.Add(dir, "web"); !errors.Is(err, r.auditEr) {
		t.Fatalf("Add err = %v, want the audit error", err)
	}
	if _, err := os.Stat(r.file()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file exists after a failed audit: %v", err)
	}
	if got := r.svc.Paths(); len(got) != 0 {
		t.Errorf("Paths = %v after a failed add", got)
	}
	// Remove fails closed too.
	r.auditEr = nil
	if _, err := r.svc.Add(dir, "web"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.file())
	r.auditEr = errors.New("audit down")
	if err := r.svc.Remove(dir, "web"); !errors.Is(err, r.auditEr) {
		t.Fatalf("Remove err = %v, want the audit error", err)
	}
	after, _ := os.ReadFile(r.file())
	if string(before) != string(after) || len(r.svc.Paths()) != 1 {
		t.Error("Remove changed state despite a failed audit")
	}
}

func TestAuditActions(t *testing.T) {
	r := newRig(t)
	dir := realDir(t)
	root, err := r.svc.Add(dir, "web")
	if err != nil {
		t.Fatal(err)
	}
	if root.Path != dir || root.AddedBy != "web" || !root.Added.Equal(r.clock) || !root.LastUsed.IsZero() {
		t.Errorf("Root = %+v", root)
	}
	if err := r.svc.Remove(dir, "web"); err != nil {
		t.Fatal(err)
	}
	want := []auditCall{{"created", dir, "web"}, {"deleted", dir, "web"}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("audit = %v, want %v", r.calls, want)
	}
	if got := r.svc.Paths(); len(got) != 0 {
		t.Errorf("Paths = %v after remove", got)
	}
	if err := r.svc.Remove(dir, "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Remove err = %v, want ErrNotFound", err)
	}
	if len(r.calls) != 2 {
		t.Errorf("audited a not-found remove: %v", r.calls)
	}
}

func TestAddResolvesSymlink(t *testing.T) {
	r := newRig(t)
	dir := realDir(t)
	link := filepath.Join(realDir(t), "l")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	root, err := r.svc.Add(link, "web")
	if err != nil {
		t.Fatal(err)
	}
	if root.Path != dir {
		t.Errorf("stored %q, want the resolved %q", root.Path, dir)
	}
	// Remove by the link spelling finds it too.
	if err := r.svc.Remove(link, "web"); err != nil {
		t.Errorf("Remove(link): %v", err)
	}
}

func TestTouchThrottled(t *testing.T) {
	r := newRig(t)
	dir := realDir(t)
	if _, err := r.svc.Add(dir, "web"); err != nil {
		t.Fatal(err)
	}
	lastUsed := func() time.Time {
		l, err := r.svc.List()
		if err != nil || len(l) != 1 {
			t.Fatalf("List: %v %v", l, err)
		}
		return l[0].LastUsed
	}
	inside := filepath.Join(dir, "proj")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := r.clock.Add(time.Minute)
	r.clock = t0
	r.svc.Touch(inside)
	if got := lastUsed(); !got.Equal(t0) {
		t.Fatalf("first touch: LastUsed = %v, want %v", got, t0)
	}
	stamp := statFile(r.dataDir)
	r.clock = t0.Add(10 * time.Minute)
	r.svc.Touch(inside)
	if got := lastUsed(); !got.Equal(t0) {
		t.Errorf("touch after 10m wrote: LastUsed = %v", got)
	}
	if statFile(r.dataDir) != stamp {
		t.Error("the file was rewritten inside the throttle window")
	}
	t1 := t0.Add(61 * time.Minute)
	r.clock = t1
	r.svc.Touch(inside)
	if got := lastUsed(); !got.Equal(t1) {
		t.Errorf("touch after 61m: LastUsed = %v, want %v", got, t1)
	}
	if statFile(r.dataDir) == stamp {
		t.Error("the file was not rewritten after the window")
	}
	if len(r.calls) != 1 {
		t.Errorf("Touch is audited: %v", r.calls)
	}
}

func TestTouchIgnoresHomeAndUnrelated(t *testing.T) {
	r := newRig(t)
	r.svc.Touch(filepath.Join(r.home, "x"))
	r.svc.Touch(realDir(t))
	if _, err := os.Stat(r.file()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Touch wrote the file for home or an unrelated path: %v", err)
	}
}

func TestHandEditSeen(t *testing.T) {
	r := newRig(t)
	a, b := realDir(t), realDir(t)
	if _, err := r.svc.Add(a, "web"); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.Paths(); !reflect.DeepEqual(got, []string{a}) {
		t.Fatalf("Paths = %v", got)
	}
	if err := Save(r.dataDir, []Root{{Path: b, AddedBy: "hand"}}); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.Paths(); !reflect.DeepEqual(got, []string{b}) {
		t.Errorf("after a hand edit Paths = %v, want [%s]", got, b)
	}
	// A hand-edit to a damaged file is seen, and a fix after it is seen too.
	if err := os.WriteFile(r.file(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.List(); !errors.Is(err, ErrCorrupt) {
		t.Errorf("List err = %v, want ErrCorrupt", err)
	}
	if err := Save(r.dataDir, []Root{{Path: a}}); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.Paths(); !reflect.DeepEqual(got, []string{a}) {
		t.Errorf("after the fix Paths = %v", got)
	}
	// A hand-deleted file means no roots.
	if err := os.Remove(r.file()); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.Paths(); len(got) != 0 {
		t.Errorf("after delete Paths = %v", got)
	}
}

func TestAddSeesHandEditBeforeWriting(t *testing.T) {
	r := newRig(t)
	a, b := realDir(t), realDir(t)
	if _, err := r.svc.Add(a, "web"); err != nil {
		t.Fatal(err)
	}
	if err := Save(r.dataDir, []Root{{Path: a}, {Path: realDir(t)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(b, "web"); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(r.dataDir)
	if err != nil || len(got) != 3 {
		t.Errorf("file has %d roots (%v), want the hand-added one kept", len(got), err)
	}
}

func TestNilAuditAndClock(t *testing.T) {
	dataDir, home, dir := realDir(t), realDir(t), realDir(t)
	svc := NewService(dataDir, home, nil, nil)
	root, err := svc.Add(dir, "web")
	if err != nil || root.Added.IsZero() {
		t.Fatalf("Add: %+v %v", root, err)
	}
}

func TestErrorsText(t *testing.T) {
	e := &RefusedError{Reason: "it is a system folder"}
	if e.Error() != "it is a system folder" {
		t.Errorf("Error() = %q", e.Error())
	}
}
