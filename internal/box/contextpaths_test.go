package box

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func ctxTmp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func wantCtxErr(t *testing.T, err error, p string, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("CheckContextPath(%q) = nil, want an error containing %q", p, parts)
	}
	if !errors.Is(err, ErrUnboxableWorkspace) {
		t.Errorf("error must wrap ErrUnboxableWorkspace: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "context_paths "+p+": ") {
		t.Errorf("error must start with %q: %v", "context_paths "+p+": ", err)
	}
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q lacks %q", err, part)
		}
	}
}

func TestCheckContextPathOK(t *testing.T) {
	tmp := ctxTmp(t)
	file := filepath.Join(tmp, "notes.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{tmp, file} {
		if err := CheckContextPath(p, "/nonexistent-home", nil, nil); err != nil {
			t.Errorf("CheckContextPath(%q): %v", p, err)
		}
	}
}

func TestCheckContextPathMissing(t *testing.T) {
	p := filepath.Join(ctxTmp(t), "nope")
	wantCtxErr(t, CheckContextPath(p, "/h", nil, nil), p, "does not exist")
}

func TestCheckContextPathSymlinkComponent(t *testing.T) {
	tmp := ctxTmp(t)
	if err := os.MkdirAll(filepath.Join(tmp, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "real", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(tmp, "b", "docs")
	if err := os.Symlink(filepath.Join(tmp, "real"), leaf); err != nil {
		t.Fatal(err)
	}
	wantCtxErr(t, CheckContextPath(leaf, "/h", nil, nil), leaf, leaf+" is a symlink")

	// The link is a middle component: it, not the leaf, is named.
	mid := filepath.Join(leaf, "inner")
	wantCtxErr(t, CheckContextPath(mid, "/h", nil, nil), mid, leaf+" is a symlink")
}

func TestCheckContextPathNotRegular(t *testing.T) {
	p := filepath.Join(ctxTmp(t), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	wantCtxErr(t, CheckContextPath(p, "/h", nil, nil), p, "must be a directory or a regular file")
}

func TestCheckContextPathHome(t *testing.T) {
	tmp := ctxTmp(t)
	home := filepath.Join(tmp, "u", "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	wantCtxErr(t, CheckContextPath(home, home, nil, nil), home, "your home directory")
	parent := filepath.Join(tmp, "u")
	wantCtxErr(t, CheckContextPath(parent, home, nil, nil), parent, "contains your home directory")
	// A sibling of home is fine.
	sib := filepath.Join(tmp, "u", "other")
	if err := os.Mkdir(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckContextPath(sib, home, nil, nil); err != nil {
		t.Errorf("sibling of home: %v", err)
	}
}

func TestCheckContextPathProtected(t *testing.T) {
	tmp := ctxTmp(t)
	data := filepath.Join(tmp, "data")
	sub := filepath.Join(data, "boxes")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wantCtxErr(t, CheckContextPath(sub, "/h", []ProtectedDir{DataDirProtected(data)}, nil), sub, "agent-box's data directory")
}

func TestCheckContextPathShared(t *testing.T) {
	tmp := ctxTmp(t)
	wantCtxErr(t, CheckContextPath(tmp, "/h", nil, []string{"/Users/u"}), tmp,
		"not under the box VM's shared directories", "agent-box box setup -recreate")
	if err := CheckContextPath(tmp, "/h", nil, nil); err != nil {
		t.Errorf("with no shared directories the check must not run: %v", err)
	}
	if err := CheckContextPath(tmp, "/h", nil, []string{tmp}); err != nil {
		t.Errorf("under a shared directory: %v", err)
	}
}

func TestSecretFiles(t *testing.T) {
	d := ctxTmp(t)
	mk := func(rel string) {
		t.Helper()
		p := filepath.Join(d, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{".env", "sub/.env.prd", "sub/k.pem", "sub/x.tfstate", "readme.md",
		"sub/deep/.env", ".git/.env", "sub/.git/k.pem"} {
		mk(rel)
	}
	if err := os.Symlink(filepath.Join(d, "readme.md"), filepath.Join(d, "sub", ".env.link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d, "readme.md"), filepath.Join(d, "id_rsa")); err != nil {
		t.Fatal(err)
	}
	got := SecretFiles(d)
	want := []string{".env", "sub/.env.prd", "sub/k.pem", "sub/x.tfstate"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("SecretFiles = %v, want %v", got, want)
	}
}

func TestSecretFilesBounded(t *testing.T) {
	d := ctxTmp(t)
	for i := range 6000 {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "zzz.env.pem"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := SecretFiles(d); len(out) > 20 {
		t.Fatalf("SecretFiles returned %d results, want at most 20", len(out))
	}
}

func TestSecretFilesResultCap(t *testing.T) {
	d := ctxTmp(t)
	for i := range 40 {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("a%02d.pem", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out := SecretFiles(d); len(out) != 20 {
		t.Fatalf("SecretFiles returned %d results, want 20", len(out))
	}
}

func TestSecretFilesStopsAtEntryCap(t *testing.T) {
	d := ctxTmp(t)
	for i := range 5100 {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// "a.pem" sorts first (visited); "zzz.pem" sorts after the 5000th entry.
	for _, n := range []string{"a.pem", "zzz.pem"} {
		if err := os.WriteFile(filepath.Join(d, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out := SecretFiles(d); !slices.Equal(out, []string{"a.pem"}) {
		t.Fatalf("SecretFiles = %v, want only the entry inside the cap", out)
	}
}
