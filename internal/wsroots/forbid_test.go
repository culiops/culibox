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
