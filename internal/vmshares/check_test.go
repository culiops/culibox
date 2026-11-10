package vmshares

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kindOf(err error) string {
	var r *Refused
	if errors.As(err, &r) {
		return r.Kind
	}
	return ""
}

func testEnv(t *testing.T, fold bool) Env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "u", "me")
	data := filepath.Join(base, "d", "culibox")
	for _, d := range []string{".ssh", ".aws", ".config/gcloud", "Library/Mobile Documents", ".claude/projects/-a-b/memory", "code"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(data, "boxstate"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Env{Home: home, DataRoot: data, Fold: fold}
}

func TestExpand(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"~", "/h"}, {"~/a/../b", "/h/b"}, {"/x/", "/x"},
	} {
		got, err := Expand(c.in, "/h")
		if err != nil || got != c.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := Expand("code/x", "/h"); kindOf(err) != KindRelative {
		t.Errorf("relative: %v", err)
	}
}

func TestWithin(t *testing.T) {
	if Within("/a/bc", "/a/b", false) || !Within("/a/b/c", "/a/b", false) || !Within("/a/b", "/a/b", false) {
		t.Error("component-wise Within wrong")
	}
	if !Within("/A/B", "/a/b", true) || Within("/A/B", "/a/b", false) {
		t.Error("fold wrong")
	}
}

func TestCheckRefusals(t *testing.T) {
	env := testEnv(t, false)
	h := env.Home
	file := filepath.Join(h, "code", "f.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ p, kind string }{
		{"/", KindRoot},
		{h, KindHome},
		{filepath.Dir(h), KindContainsHome},
		{h + "/.ssh", KindSensitive},
		{h + "/.ssh/x", KindSensitive},
		{h + "/Library/Mobile Documents", KindSensitive},
		{h + "/.config", KindSensitive},
		{h + "/.claude", KindSensitive},
		{h + "/.claude/projects", KindSensitive},
		{h + "/.claude/projects/-a-b/memory", ""},
		{h + "/.claude/projects/-a-b/memory/x", ""},
		{env.DataRoot, KindData},
		{env.DataRoot + "/boxstate", KindData},
		{filepath.Dir(env.DataRoot), KindData},
		{h + "/co,de", KindUnsafe},
		{h + `/co"de`, KindUnsafe},
		{file, KindNotDir},
		{h + "/code/missing", ""},
		{h + "/code", ""},
		{"code/x", KindRelative},
	} {
		if got := kindOf(Check(c.p, env)); got != c.kind {
			t.Errorf("Check(%q) kind = %q, want %q", c.p, got, c.kind)
		}
	}
	err := Check(h+"/.ssh", env)
	if want := "~/.ssh holds keys and sockets, so it can never be shared with the box VM."; err == nil || err.Error() != want {
		t.Errorf("Error() = %v, want %q", err, want)
	}
}

func TestCheckFoldsCaseOnDarwin(t *testing.T) {
	env := testEnv(t, true)
	for _, p := range []string{env.Home + "/.SSH", env.Home + "/library", strings.ToUpper(env.Home)} {
		if Check(p, env) == nil {
			t.Errorf("Fold: %q should be refused", p)
		}
	}
	env.Fold = false
	if err := Check(env.Home+"/.SSH", env); err != nil {
		t.Errorf("no fold: ~/.SSH should be allowed, got %v", err)
	}
}

func TestCheckResolvesSymlinks(t *testing.T) {
	env := testEnv(t, false)
	link := filepath.Join(env.Home, "proj")
	if err := os.Symlink(filepath.Join(env.Home, ".ssh"), link); err != nil {
		t.Fatal(err)
	}
	err := Check(link, env)
	var r *Refused
	if !errors.As(err, &r) || r.Kind != KindSensitive || r.Path != link {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "~/proj ") {
		t.Errorf("Error() = %q", err)
	}
}
