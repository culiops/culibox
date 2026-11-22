package linktrust

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/datacompat"
)

type auditCall struct {
	action, host string
	subdomains   bool
	actor        string
}

type rig struct {
	t       *testing.T
	dataDir string
	calls   []auditCall
	auditEr error
	clock   time.Time
	svc     *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, clock: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC), dataDir: t.TempDir()}
	r.svc = NewService(r.dataDir, func(action, host string, subdomains bool, actor string) error {
		r.calls = append(r.calls, auditCall{action, host, subdomains, actor})
		return r.auditEr
	}, func() time.Time { return r.clock })
	return r
}

func (r *rig) file() string { return filepath.Join(r.dataDir, FileName) }

func (r *rig) hosts(actor string) []string {
	r.t.Helper()
	list, err := r.svc.List(actor)
	if err != nil {
		r.t.Fatalf("List(%q): %v", actor, err)
	}
	out := []string{}
	for _, h := range list {
		s := h.Host
		if h.Subdomains {
			s += "+"
		}
		out = append(out, s)
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestListEmpty(t *testing.T) {
	r := newRig(t)
	list, err := r.svc.List("web")
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("List = %v, %v; want empty non-nil", list, err)
	}
	if _, err := os.Stat(r.file()); err == nil {
		t.Error("List created the file")
	}
}

func TestAddListRemove(t *testing.T) {
	r := newRig(t)
	h, err := r.svc.Add("github.com", true, "web")
	if err != nil {
		t.Fatal(err)
	}
	if h.Host != "github.com" || !h.Subdomains || h.Actor != "web" || !h.Added.Equal(r.clock) {
		t.Errorf("Add returned %+v", h)
	}
	if _, err := r.svc.Add("docs.example.com", false, "web"); err != nil {
		t.Fatal(err)
	}
	if got := r.hosts("web"); !eq(got, []string{"github.com+", "docs.example.com"}) {
		t.Errorf("hosts = %v", got)
	}
	// A new service over the same dir reads what was saved.
	again := NewService(r.dataDir, nil, nil)
	list, err := again.List("web")
	if err != nil || len(list) != 2 || list[0].Host != "github.com" || !list[0].Subdomains {
		t.Errorf("fresh service List = %+v, %v", list, err)
	}
	if err := r.svc.Remove("github.com", "web"); err != nil {
		t.Fatal(err)
	}
	if got := r.hosts("web"); !eq(got, []string{"docs.example.com"}) {
		t.Errorf("after remove: %v", got)
	}
	want := []auditCall{
		{"created", "github.com", true, "web"},
		{"created", "docs.example.com", false, "web"},
		{"deleted", "github.com", true, "web"},
	}
	if fmt.Sprint(r.calls) != fmt.Sprint(want) {
		t.Errorf("audit calls = %v, want %v", r.calls, want)
	}
	if err := r.svc.Remove("github.com", "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove of a missing host: %v, want ErrNotFound", err)
	}
	if len(r.calls) != 3 {
		t.Errorf("a failed Remove was audited: %v", r.calls)
	}
}

func TestPerActorIsolation(t *testing.T) {
	r := newRig(t)
	for _, a := range []struct {
		host, actor string
		sub         bool
	}{
		{"github.com", "alice", true},
		{"github.com", "bob", false},
		{"only-alice.example.com", "alice", false},
	} {
		if _, err := r.svc.Add(a.host, a.sub, a.actor); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.hosts("alice"); !eq(got, []string{"github.com+", "only-alice.example.com"}) {
		t.Errorf("alice = %v", got)
	}
	if got := r.hosts("bob"); !eq(got, []string{"github.com"}) {
		t.Errorf("bob = %v", got)
	}
	if got := r.hosts("carol"); len(got) != 0 {
		t.Errorf("carol = %v", got)
	}
	// Bob cannot remove Alice's row, and removing his own leaves hers.
	if err := r.svc.Remove("only-alice.example.com", "bob"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob removing alice's host: %v, want ErrNotFound", err)
	}
	if err := r.svc.Remove("github.com", "bob"); err != nil {
		t.Fatal(err)
	}
	if got := r.hosts("alice"); !eq(got, []string{"github.com+", "only-alice.example.com"}) {
		t.Errorf("alice after bob's remove = %v", got)
	}
	// Bob re-adding with a different flag is his own row, not an update of Alice's.
	if _, err := r.svc.Add("github.com", true, "bob"); err != nil {
		t.Fatal(err)
	}
	last := r.calls[len(r.calls)-1]
	if last.action != "created" || last.actor != "bob" {
		t.Errorf("last audit = %+v, want created by bob", last)
	}
}

func TestAddNoOpAndUpdate(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Add("github.com", false, "web"); err != nil {
		t.Fatal(err)
	}
	added := r.clock
	r.clock = r.clock.Add(time.Hour)
	before, _ := os.Stat(r.file())
	// Same host, same flag: no-op, no audit, no write.
	h, err := r.svc.Add("github.com", false, "web")
	if err != nil || !h.Added.Equal(added) {
		t.Fatalf("no-op Add = %+v, %v", h, err)
	}
	if len(r.calls) != 1 {
		t.Errorf("no-op was audited: %v", r.calls)
	}
	after, _ := os.Stat(r.file())
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("no-op rewrote the file")
	}
	// Same host, other flag: an update, audited "updated", keeping its Added.
	h, err = r.svc.Add("github.com", true, "web")
	if err != nil || !h.Subdomains || !h.Added.Equal(added) {
		t.Fatalf("update Add = %+v, %v", h, err)
	}
	if want := (auditCall{"updated", "github.com", true, "web"}); len(r.calls) != 2 || r.calls[1] != want {
		t.Errorf("audit calls = %v", r.calls)
	}
	if got := r.hosts("web"); !eq(got, []string{"github.com+"}) {
		t.Errorf("hosts = %v", got)
	}
}

func TestAddRefused(t *testing.T) {
	r := newRig(t)
	long := func(labels ...int) string {
		var parts []string
		for _, n := range labels {
			parts = append(parts, strings.Repeat("a", n))
		}
		return strings.Join(parts, ".")
	}
	for name, host := range map[string]string{
		"empty":           "",
		"ipv4":            "192.168.1.10",
		"ipv6":            "::1",
		"ipv6 brackets":   "[::1]",
		"ipv6 long":       "2001:db8::1",
		"punycode":        "xn--gthub-zsa.com",
		"punycode sub":    "www.xn--mller-kva.de",
		"port":            "example.com:8080",
		"uppercase":       "Example.com",
		"single label":    "localhost",
		"trailing dot":    "example.com.",
		"leading dot":     ".example.com",
		"empty label":     "a..example.com",
		"over 253":        long(63, 63, 63, 62),
		"label over 63":   long(64) + ".com",
		"scheme":          "https://example.com",
		"path":            "example.com/x",
		"userinfo":        "user@example.com",
		"query":           "example.com?x=1",
		"space":           "exa mple.com",
		"underscore":      "my_host.example.com",
		"non ascii":       "gíthub.com",
		"leading hyphen":  "-a.example.com",
		"trailing hyphen": "a-.example.com",
		"numeric tld":     "example.123",
		"hex tld":         "example.0x1f",
		"nul":             "example.com\x00",
		"newline":         "example.com\n",
		"wildcard":        "*.example.com",
	} {
		_, err := r.svc.Add(host, false, "web")
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s (%q): err = %v, want ErrRefused", name, host, err)
		}
	}
	if _, serr := os.Stat(r.file()); serr == nil {
		t.Error("file written for a refused add")
	}
	if len(r.calls) != 0 {
		t.Errorf("audited a refused add: %v", r.calls)
	}
}

func TestAddAccepts(t *testing.T) {
	r := newRig(t)
	long := func(labels ...int) string {
		var parts []string
		for _, n := range labels {
			parts = append(parts, strings.Repeat("a", n))
		}
		return strings.Join(parts, ".")
	}
	for _, host := range []string{
		"github.com", "docs.example.com", "a-b.example.co.uk", "a1.b2.c3", "x.io",
		long(63, 63, 63, 61), long(63) + ".com", "1.example.com", "example.c0m",
	} {
		if _, err := r.svc.Add(host, false, "web"); err != nil {
			t.Errorf("Add(%q): %v", host, err)
		}
	}
}

func TestCapPerActor(t *testing.T) {
	r := newRig(t)
	for i := 0; i < MaxPerActor; i++ {
		if _, err := r.svc.Add(fmt.Sprintf("h%d.example.com", i), false, "web"); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	n := len(r.calls)
	if _, err := r.svc.Add("one-too-many.example.com", false, "web"); !errors.Is(err, ErrCap) {
		t.Fatalf("err = %v, want ErrCap", err)
	}
	if len(r.calls) != n || len(r.hosts("web")) != MaxPerActor {
		t.Error("a capped add was audited or saved")
	}
	// At the cap, a no-op and an update still work, and another actor has its own 200.
	if _, err := r.svc.Add("h0.example.com", false, "web"); err != nil {
		t.Errorf("no-op at cap: %v", err)
	}
	if _, err := r.svc.Add("h0.example.com", true, "web"); err != nil {
		t.Errorf("update at cap: %v", err)
	}
	if _, err := r.svc.Add("one-too-many.example.com", false, "other"); err != nil {
		t.Errorf("other actor at cap: %v", err)
	}
	if err := r.svc.Remove("h1.example.com", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add("one-too-many.example.com", false, "web"); err != nil {
		t.Errorf("add after a remove: %v", err)
	}
}

func TestAuditFailureSavesNothing(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Add("keep.example.com", false, "web"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(r.file())
	r.auditEr = errors.New("audit store: disk full")
	if _, err := r.svc.Add("new.example.com", false, "web"); !errors.Is(err, r.auditEr) {
		t.Errorf("Add err = %v, want the audit error", err)
	}
	if _, err := r.svc.Add("keep.example.com", true, "web"); !errors.Is(err, r.auditEr) {
		t.Errorf("update err = %v, want the audit error", err)
	}
	if err := r.svc.Remove("keep.example.com", "web"); !errors.Is(err, r.auditEr) {
		t.Errorf("Remove err = %v, want the audit error", err)
	}
	after, _ := os.ReadFile(r.file())
	if string(before) != string(after) {
		t.Errorf("file changed: %s -> %s", before, after)
	}
	if got := r.hosts("web"); !eq(got, []string{"keep.example.com"}) {
		t.Errorf("in-memory list changed: %v", got)
	}
}

// The audit row is written before the file: at the moment the audit runs, the
// file does not have the new host yet.
func TestAuditRunsBeforeSave(t *testing.T) {
	dir := t.TempDir()
	var sawFile bool
	svc := NewService(dir, func(string, string, bool, string) error {
		_, err := os.Stat(filepath.Join(dir, FileName))
		sawFile = err == nil
		return nil
	}, nil)
	if _, err := svc.Add("github.com", false, "web"); err != nil {
		t.Fatal(err)
	}
	if sawFile {
		t.Error("the file existed when the audit ran")
	}
}

func TestCorruptFileNeverOverwritten(t *testing.T) {
	r := newRig(t)
	if err := os.WriteFile(r.file(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.List("web"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("List err = %v, want ErrCorrupt", err)
	}
	if _, err := r.svc.Add("github.com", false, "web"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Add err = %v, want ErrCorrupt", err)
	}
	if err := r.svc.Remove("github.com", "web"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Remove err = %v, want ErrCorrupt", err)
	}
	b, err := os.ReadFile(r.file())
	if err != nil || string(b) != "{" {
		t.Errorf("file = %q, %v; want untouched", b, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("audited on a corrupt file: %v", r.calls)
	}
}

func TestNewerFileNeverOverwritten(t *testing.T) {
	r := newRig(t)
	const body = `{"version":2,"hosts":[{"id":7}]}`
	if err := os.WriteFile(r.file(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := r.svc.List("web")
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, datacompat.ErrNewer) {
		t.Errorf("List err = %v, want ErrCorrupt and ErrNewer", err)
	}
	if _, err := r.svc.Add("github.com", false, "web"); !errors.Is(err, datacompat.ErrNewer) {
		t.Errorf("Add err = %v, want ErrNewer", err)
	}
	if b, _ := os.ReadFile(r.file()); string(b) != body {
		t.Errorf("file = %q; want untouched", b)
	}
}

// A hand edit is seen on the next call, and a file fixed after corruption
// recovers without a restart.
func TestReloadsOnChange(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Add("github.com", false, "web"); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"hosts":[{"actor":"web","host":"gitlab.com","subdomains":true,"added":"2026-01-01T00:00:00Z"},` +
		`{"actor":"web","host":"example.org","subdomains":false,"added":"2026-01-01T00:00:00Z"}]}` + "\n"
	if err := os.WriteFile(r.file(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.hosts("web"); !eq(got, []string{"gitlab.com+", "example.org"}) {
		t.Errorf("after hand edit: %v", got)
	}
	if err := os.WriteFile(r.file(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.List("web"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if err := os.WriteFile(r.file(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.hosts("web"); len(got) != 2 {
		t.Errorf("after the file was fixed: %v", got)
	}
}

func TestListReturnsACopy(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Add("github.com", false, "web"); err != nil {
		t.Fatal(err)
	}
	list, _ := r.svc.List("web")
	list[0].Host = "evil.example.com"
	if got := r.hosts("web"); !eq(got, []string{"github.com"}) {
		t.Errorf("a caller changed the service's list: %v", got)
	}
}
