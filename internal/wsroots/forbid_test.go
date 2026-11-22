package wsroots

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefusalClasses(t *testing.T) {
	env := Env{Home: "/home/u", DataDir: "/home/u/.local/share/culibox"}
	const (
		root     = "it is the filesystem root"
		system   = "it is a system folder"
		credsIn  = "it holds credentials"
		credsAnc = "it contains your credential folders"
		data     = "it holds CuliBox's own data"
	)
	rows := []struct {
		path, want string
	}{
		{"/", root},
		{"/etc", system},
		{"/etc/nginx", system},
		{"/usr/local/src/x", system},
		{"/srv", ""},
		{"/var", system},
		{"/var/www/app", ""},
		{"/home/u/.ssh", credsIn},
		{"/home/u/.aws/sub", credsIn},
		{"/home/u/.gnupg", credsIn},
		{"/home", credsAnc},
		{"/home/u", credsAnc},
		{"/home/u/.local/share/culibox/x", data},
		{"/home/u/.local/share/culibox", data},
		{"/home/u/.local", data},
		{"/opt/proj", ""},
		{"/home/u/code", ""},
		// component-wise: a sibling that only shares a prefix is not inside.
		{"/etcetera", ""},
		{"/home/u/.sshx", ""},
		{"/variety", ""},
	}
	for _, r := range rows {
		if got := Refusal(r.path, env); got != r.want {
			t.Errorf("Refusal(%q) = %q, want %q", r.path, got, r.want)
		}
	}
}

func TestRefusalNoHomeNoDataDir(t *testing.T) {
	// Empty Home and DataDir add no rules (and never match everything).
	if got := Refusal("/opt/proj", Env{}); got != "" {
		t.Fatalf("Refusal(/opt/proj, empty env) = %q, want allowed", got)
	}
	if got := Refusal("/etc", Env{}); got != "it is a system folder" {
		t.Fatalf("Refusal(/etc, empty env) = %q", got)
	}
}

func TestRefusalVarContains(t *testing.T) {
	// /var is refused when the candidate contains it; "/private" contains
	// /private/var, the macOS spelling.
	if got := refusalWith("/private", nil, []string{"/private/var"}); got != "it contains a system folder" {
		t.Fatalf("refusalWith(/private) = %q", got)
	}
	if got := refusalWith("/private/var/www", nil, []string{"/private/var"}); got != "" {
		t.Fatalf("inside /private/var must be allowed, got %q", got)
	}
}

func TestRefusalResolvedBase(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(tmp, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if got := refusalWith(filepath.Join(real, "sub"), []string{alias}, nil); got != "it is a system folder" {
		t.Fatalf("path inside the resolved base: got %q", got)
	}
	if got := refusalWith(real, []string{alias}, nil); got != "it is a system folder" {
		t.Fatalf("the resolved base itself: got %q", got)
	}
	if got := refusalWith(tmp, []string{alias}, nil); got != "it contains a system folder" {
		t.Fatalf("an ancestor of the resolved base: got %q", got)
	}
	if got := refusalWith(filepath.Join(tmp, "other"), []string{alias}, nil); got != "" {
		t.Fatalf("unrelated path: got %q", got)
	}
}

// M3: macOS volumes ignore case by default, so on darwin /ETC is /etc and
// ~/.SSH is ~/.ssh. The toggle lets this run on Linux too.
func TestRefusalFoldsCaseOnDarwin(t *testing.T) {
	env := Env{Home: "/Users/u", DataDir: "/Users/u/Library/Application Support/culibox"}
	rows := []struct{ path, want string }{
		{"/ETC", "it is a system folder"},
		{"/Etc/hosts.d", "it is a system folder"},
		{"/usr/LOCAL/src", "it is a system folder"},
		{"/VAR", "it is a system folder"},
		{"/var/WWW/app", ""},
		{"/users/u/.SSH", "it holds credentials"},
		{"/users/U/.Aws/sub", "it holds credentials"},
		{"/USERS", "it contains your credential folders"},
		{"/users/u/library/application support/CULIBOX", "it holds CuliBox's own data"},
		{"/Users/u/Code", ""},
		{"/ETCETERA", ""},
	}
	old := foldCase
	t.Cleanup(func() { foldCase = old })
	foldCase = true
	for _, r := range rows {
		if got := Refusal(r.path, env); got != r.want {
			t.Errorf("fold: Refusal(%q) = %q, want %q", r.path, got, r.want)
		}
	}
	if got := refusalWith("/PRIVATE", nil, []string{"/private/var"}); got != "it contains a system folder" {
		t.Errorf("fold: refusalWith(/PRIVATE) = %q", got)
	}
	// Linux paths are case-sensitive: /ETC is some other folder there.
	foldCase = false
	if got := Refusal("/ETC", env); got != "" {
		t.Errorf("no fold: Refusal(/ETC) = %q, want allowed", got)
	}
	if got := Refusal("/users/u/.SSH", env); got != "" {
		t.Errorf("no fold: Refusal(/users/u/.SSH) = %q, want allowed", got)
	}
}

// M6: the fixed system folders are resolved once per process, not on every
// call: a call resolves only the per-Env folders (three credential folders and
// the data dir).
func TestRefusalResolvesSystemBasesOnce(t *testing.T) {
	var n int
	old := evalSymlinks
	t.Cleanup(func() { evalSymlinks = old })
	evalSymlinks = func(p string) (string, error) { n++; return old(p) }
	env := Env{Home: "/home/u", DataDir: "/home/u/.local/share/culibox"}
	Refusal("/opt/proj", env) // warm up: the first call may resolve the system set
	n = 0
	for i := 0; i < 10; i++ {
		Refusal("/opt/proj", env)
	}
	if want := 10 * (len(credentialDirs) + 1); n != want {
		t.Fatalf("10 calls resolved %d paths, want %d (per-Env folders only)", n, want)
	}
}
