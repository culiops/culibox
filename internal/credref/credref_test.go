package credref

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/secretstore"
)

const fakeSecret = "tok-SECRET-1234"

func TestValueTrimAndRefuse(t *testing.T) {
	kept := map[string]string{"abc\n": "abc", "abc\r\n": "abc", "abc": "abc"}
	for in, want := range kept {
		got, err := Value([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("Value(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "\n", "abc\n\n", "a\tb", "a\x7fb", "a\rb"} {
		got, err := Value([]byte(in))
		if err == nil {
			t.Errorf("Value(%q) = %q; want error", in, got)
		}
	}
	if _, err := Value([]byte("a\tb")); err == nil || !strings.Contains(err.Error(), "contains a control character") {
		t.Errorf("control character message: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFileRefusals(t *testing.T) {
	dir := t.TempDir()
	loose := writeFile(t, dir, "loose", fakeSecret, 0o644)
	good := writeFile(t, dir, "good", fakeSecret, 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path string
		max        int64
		want       string
	}{
		{"mode", loose, 1024, "mode"},
		{"symlink", link, 1024, "symlink"},
		{"directory", sub, 1024, ""},
		{"too large", good, 4, "too large"},
		{"relative", "relative", 1024, "absolute"},
		{"missing", filepath.Join(dir, "missing"), 1024, ""},
	}
	for _, c := range cases {
		_, err := ReadFile(c.path, c.max)
		if err == nil {
			t.Errorf("%s: want error", c.name)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q lacks %q", c.name, err, c.want)
		}
		if strings.Contains(err.Error(), fakeSecret) {
			t.Errorf("%s: error leaks the value: %v", c.name, err)
		}
	}

	got, err := ReadFile(good, 1024)
	if err != nil || string(got) != fakeSecret {
		t.Fatalf("good: %q, %v", got, err)
	}
}

func TestDefaultResolveEnv(t *testing.T) {
	d := Default{LookupEnv: func(k string) (string, bool) {
		if k == "MY_TOKEN" {
			return fakeSecret + "\n", true
		}
		if k == "EMPTY_TOKEN" {
			return "", true
		}
		return "", false
	}}
	got, err := d.Resolve(config.KeyRef{Type: "env", Var: "MY_TOKEN"})
	if err != nil || string(got) != fakeSecret {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, v := range []string{"UNSET_TOKEN", "EMPTY_TOKEN"} {
		_, err = d.Resolve(config.KeyRef{Type: "env", Var: v})
		if err == nil || !strings.Contains(err.Error(), v) || strings.Contains(err.Error(), fakeSecret) {
			t.Fatalf("%s: unset error = %v", v, err)
		}
	}
	if _, err = d.Resolve(config.KeyRef{Type: "env", Var: "AB_PASS"}); err == nil {
		t.Fatal("AB_PASS must be refused")
	}
	if _, err = d.Resolve(config.KeyRef{Type: "store"}); err == nil {
		t.Fatal("store ref without an id must be refused")
	}
}

func TestDefaultResolveNilLookupUsesOS(t *testing.T) {
	t.Setenv("CREDREF_TEST_TOKEN", fakeSecret)
	got, err := Default{}.Resolve(config.KeyRef{Type: "env", Var: "CREDREF_TEST_TOKEN"})
	if err != nil || string(got) != fakeSecret {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveFileTrimsAndRefusesControl(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "t", fakeSecret+"\n", 0o600)
	got, err := Default{}.Resolve(config.KeyRef{Type: "file", Path: p})
	if err != nil || string(got) != fakeSecret {
		t.Fatalf("got %q, %v", got, err)
	}
	bad := writeFile(t, dir, "bad", "a\tb\n", 0o600)
	if _, err := (Default{}).Resolve(config.KeyRef{Type: "file", Path: bad}); err == nil {
		t.Fatal("control character must be refused")
	}
}

func TestResolveFilePicksUpRotation(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "t", "v1\n", 0o600)
	ref := config.KeyRef{Type: "file", Path: p}
	d := Default{}
	got, err := d.Resolve(ref)
	if err != nil || string(got) != "v1" {
		t.Fatalf("v1: %q, %v", got, err)
	}
	tmp := writeFile(t, dir, "t.new", "v2\n", 0o600)
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	got, err = d.Resolve(ref)
	if err != nil || string(got) != "v2" {
		t.Fatalf("v2: %q, %v", got, err)
	}
}

type fakeStore struct {
	val   []byte
	err   error
	calls int
}

func (f *fakeStore) Get(id string) ([]byte, error) { f.calls++; return f.val, f.err }

func TestResolveStore(t *testing.T) {
	ref := config.KeyRef{Type: "store", ID: "tok"}
	fs := &fakeStore{val: []byte("tok\n")}
	got, err := Default{Store: fs}.Resolve(ref)
	if err != nil || string(got) != "tok" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}

	fs = &fakeStore{err: ErrLocked}
	if _, err = (Default{Store: fs}).Resolve(ref); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v", err)
	}
	fs = &fakeStore{err: secretstore.ErrNoSuchValue}
	if _, err = (Default{Store: fs}).Resolve(ref); !errors.Is(err, secretstore.ErrNoSuchValue) || !strings.Contains(err.Error(), "tok") {
		t.Fatalf("missing: %v", err)
	}

	_, err = Default{}.Resolve(ref)
	if err == nil || err.Error() != "store references need the agent-box store (not configured)" {
		t.Fatalf("nil store: %v", err)
	}

	// A stored value goes through the same Value filter as file/env.
	fs = &fakeStore{val: []byte("a\nb")}
	if _, err = (Default{Store: fs}).Resolve(ref); err == nil {
		t.Fatal("control byte must be refused")
	}
}

func TestMaxValueBytesShared(t *testing.T) {
	if MaxValueBytes != secretstore.MaxValueBytes {
		t.Fatalf("credref %d != secretstore %d", MaxValueBytes, secretstore.MaxValueBytes)
	}
}
