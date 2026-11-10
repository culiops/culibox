package vmshares

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
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
		{h + "/.config/gh/x", KindSensitive},
		{h + "/.azure", KindSensitive},
		{h + "/.terraform.d", KindSensitive},
		{h + "/.password-store", KindSensitive},
		{h + "/.config/op", KindSensitive},
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

func TestCheckRefusesTargetOfSymlinkedSensitiveDir(t *testing.T) {
	env := testEnv(t, false)
	if err := os.RemoveAll(filepath.Join(env.Home, ".aws")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(env.Home, "dotfiles", "aws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(env.Home, "dotfiles", "aws"), filepath.Join(env.Home, ".aws")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{env.Home + "/dotfiles/aws", env.Home + "/dotfiles"} {
		if got := kindOf(Check(p, env)); got != KindSensitive {
			t.Errorf("Check(%q) kind = %q, want sensitive", p, got)
		}
	}
}

func TestYAMLSafe(t *testing.T) {
	for _, p := range []string{`/a"b`, `/a\b`, "/a\nb", "/a\x7fb", "/a\u0085b", "/a\u2028b", "/a\u2029b", "/a\xffb"} {
		if YAMLSafe(p) == nil {
			t.Errorf("YAMLSafe(%q) = nil, want refusal", p)
		}
	}
	if err := YAMLSafe("/Users/me/my code/é"); err != nil {
		t.Errorf("valid path refused: %v", err)
	}
}

func TestCheckRefusesProtectedDirs(t *testing.T) {
	env := testEnv(t, false)
	cfg := filepath.Join(env.Home, ".config", "agent-box")
	prof := filepath.Join(env.Home, "profiles-dir")
	bundleSrc := filepath.Join(env.Home, "my-claude")
	for _, d := range []string{filepath.Join(cfg, "sub"), prof, bundleSrc, filepath.Join(env.Home, ".claude")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env.Protected = []box.ProtectedDir{
		box.ConfigDirProtected(cfg),
		box.ProfilesDirProtected(prof),
		box.PersonalConfigDirProtected(filepath.Join(env.Home, ".claude")),
		box.PersonalConfigDirProtected(bundleSrc),
	}
	for _, p := range []string{cfg, prof, bundleSrc, filepath.Join(bundleSrc, "x")} {
		err := Check(p, env)
		if kindOf(err) != KindProtected {
			t.Errorf("Check(%s) = %v, want protected", p, err)
		}
	}
	err := Check(cfg, env)
	if err == nil || !strings.Contains(err.Error(), "holds CuliBox's own agent-box's config directory, so it can never be shared with the box VM.") {
		t.Errorf("reason: %v", err)
	}
	// Inside a non-Inside dir is allowed (a profiles file may sit directly in $HOME).
	if err := Check(filepath.Join(cfg, "sub"), env); err != nil {
		t.Errorf("inside config dir: %v", err)
	}
	// The ~/.claude memory exception survives the protected list.
	if err := Check(filepath.Join(env.Home, ".claude", "projects", "-a-b", "memory"), env); err != nil {
		t.Errorf("memory exception: %v", err)
	}
	if err := Check(filepath.Join(env.Home, ".claude"), env); kindOf(err) != KindSensitive {
		t.Errorf("~/.claude: %v", err)
	}
	if err := Check(filepath.Join(env.Home, "code"), env); err != nil {
		t.Errorf("unrelated: %v", err)
	}
	// A symlink to the config dir is refused too.
	link := filepath.Join(env.Home, "code", "cfglink")
	if err := os.Symlink(cfg, link); err != nil {
		t.Fatal(err)
	}
	if err := Check(link, env); kindOf(err) != KindProtected {
		t.Errorf("symlink: %v", err)
	}
}

// S1: colima runs os.ExpandEnv on every mount location and lima renders it as
// a Go template, so `$`, `{`, `}` and a backtick would turn a checked path into
// another host folder. All four are refused as unsafe, by Check and YAMLSafe.
func TestCheckRefusesExpansionCharacters(t *testing.T) {
	env := testEnv(t, false)
	for _, name := range []string{"$x", "${HOME}", "a{b", "a}b", "{{.Home}}", "a`b`"} {
		p := filepath.Join(env.Home, "code", name)
		if err := YAMLSafe(p); err == nil {
			t.Errorf("YAMLSafe(%q) = nil, want refusal", p)
		}
		err := Check(p, env)
		if kindOf(err) != KindUnsafe {
			t.Errorf("Check(%q) = %v, want unsafe", p, err)
			continue
		}
		if !strings.HasSuffix(err.Error(), reasonUnsafe) {
			t.Errorf("Check(%q) reason = %q, want the unsafe reason", p, err)
		}
	}
}

func TestNormalizeDropsExpansionCharacters(t *testing.T) {
	env := testEnv(t, false)
	ok := filepath.Join(env.Home, "code")
	bad := filepath.Join(env.Home, "$x")
	out, skipped := Normalize([]Share{{Path: bad, Mode: RW}, {Path: ok, Mode: RW}}, env)
	if len(out) != 1 || out[0].Path != ok {
		t.Fatalf("Normalize kept %+v, want only %s", out, ok)
	}
	if len(skipped) != 1 || skipped[0].Path != bad || skipped[0].Reason != reasonUnsafe {
		t.Fatalf("skipped = %+v, want %s as unsafe", skipped, bad)
	}
}

// S2: a macOS firmlink (/System/Volumes/Data/Users/<u> is /Users/<u>) or an
// APFS normalisation variant names the same folder with a spelling that
// symlink resolution does not change. Here a symlink stands in for one, with
// spelling resolution switched off, so only file identity can see the alias.
func TestCheckRefusesAliasesByIdentity(t *testing.T) {
	env := testEnv(t, false)
	h := env.Home
	cfg := filepath.Join(h, "conf", "agent-box")
	for _, d := range []string{filepath.Join(cfg, "sub"), filepath.Join(h, ".claude", "hooks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env.Protected = []box.ProtectedDir{
		box.ConfigDirProtected(cfg),
		box.PersonalConfigDirProtected(filepath.Join(h, ".claude")),
	}
	old := resolveSpelling
	resolveSpelling = filepath.Clean // the alias is invisible to spelling resolution, as a firmlink is
	t.Cleanup(func() { resolveSpelling = old })
	alias := filepath.Join(filepath.Dir(filepath.Dir(h)), "alias")
	if err := os.MkdirAll(alias, 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(name, target string) string {
		p := filepath.Join(alias, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	home := link("home", h)
	parent := link("parent", filepath.Dir(h))
	ssh := link("ssh", filepath.Join(h, ".ssh"))
	data := link("data", env.DataRoot)
	dataParent := link("dataparent", filepath.Dir(env.DataRoot))
	for _, c := range []struct{ p, kind string }{
		{home, KindHome},
		{parent, KindContainsHome},
		{ssh, KindSensitive},
		{filepath.Join(ssh, "x"), KindSensitive},
		{filepath.Join(home, ".ssh"), KindSensitive},
		{filepath.Join(home, ".aws", "creds"), KindSensitive},
		{filepath.Join(home, ".claude"), KindSensitive},
		{filepath.Join(home, ".claude", "hooks"), KindSensitive},
		{data, KindData},
		{filepath.Join(data, "boxstate"), KindData},
		{dataParent, KindData},
		{filepath.Join(home, "conf", "agent-box"), KindProtected},
		{filepath.Join(home, "conf"), KindProtected},
	} {
		if err := Check(c.p, env); kindOf(err) != c.kind {
			t.Errorf("Check(%s) = %v, want %s", c.p, err, c.kind)
		}
	}
	for _, p := range []string{
		filepath.Join(home, "code"),                                  // inside home is what shares are
		filepath.Join(home, ".claude", "projects", "-a-b", "memory"), // the memory exception
		filepath.Join(home, "conf", "agent-box", "sub"),              // inside a non-Inside protected dir
	} {
		if err := Check(p, env); err != nil {
			t.Errorf("Check(%s) = %v, want allowed", p, err)
		}
	}
}
