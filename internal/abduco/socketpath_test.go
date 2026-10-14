package abduco

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSocketPathFormula(t *testing.T) {
	got := SocketPath("/tmp/s", "alice", "claude-1", "mbp.local")
	if got != "/tmp/s/abduco/alice/claude-1@mbp.local" {
		t.Errorf("SocketPath = %q", got)
	}
}

func TestSocketPathLimit(t *testing.T) {
	want := 107
	if runtime.GOOS == "darwin" {
		want = 103
	}
	if SocketPathLimit() != want {
		t.Errorf("SocketPathLimit = %d, want %d", SocketPathLimit(), want)
	}
}

func TestCheckSocketPath(t *testing.T) {
	lim := SocketPathLimit()
	dir := "/tmp/s"
	fixed := len(SocketPath(dir, "u", "", "h"))
	fits := strings.Repeat("a", lim-fixed)
	if err := CheckSocketPath(dir, "u", fits, "h"); err != nil {
		t.Errorf("exactly at the limit must pass: %v", err)
	}
	err := CheckSocketPath(dir, "u", fits+"a", "h")
	if err == nil || !strings.Contains(err.Error(), "abduco_socket_dir") {
		t.Errorf("one byte over must fail naming abduco_socket_dir, got %v", err)
	}
}

// Review focus #5: the limit is in bytes; a multibyte name must not slip under.
func TestCheckSocketPath_CountsBytes(t *testing.T) {
	lim := SocketPathLimit()
	dir := "/tmp/s"
	fixed := len(SocketPath(dir, "u", "", "h"))
	runes := strings.Repeat("â", lim-fixed) // 2 bytes each: rune count fits, byte count does not
	if err := CheckSocketPath(dir, "u", runes, "h"); err == nil {
		t.Errorf("multibyte id over the byte limit must fail")
	}
}

func TestRepresentativeID(t *testing.T) {
	if got := RepresentativeID([]string{"zsh", "claude-opus", "sh"}); got != "claude-opus-9999" {
		t.Errorf("RepresentativeID = %q", got)
	}
	if got := RepresentativeID(nil); got != "agent-9999" {
		t.Errorf("RepresentativeID(nil) = %q", got)
	}
}

func TestDefaultSocketDir(t *testing.T) {
	if got := DefaultSocketDir("linux", "/home/u/.local/share/agent-box", 1000); got != "/home/u/.local/share/agent-box/abduco" {
		t.Errorf("linux default changed: %q (would orphan live sessions)", got)
	}
	a := DefaultSocketDir("darwin", "/Users/u/.local/share/agent-box", 501)
	b := DefaultSocketDir("darwin", "/Users/u/other", 501)
	if !strings.HasPrefix(a, "/tmp/agent-box-501-") || len(a) != len("/tmp/agent-box-501-")+8 {
		t.Errorf("darwin default = %q", a)
	}
	if a == b {
		t.Errorf("two data dirs share a socket dir: %q", a)
	}
	if SocketDirFor("/x/y", "darwin", "/d", 1) != "/x/y" {
		t.Errorf("configured dir must win")
	}
}

func TestCheckSocketDir(t *testing.T) {
	uid := os.Getuid()
	base := t.TempDir()

	ok := filepath.Join(base, "ok")
	if err := PrepareSocketDir(ok, uid); err != nil {
		t.Fatalf("fresh dir: %v", err)
	}
	if err := PrepareSocketDir(ok, uid); err != nil {
		t.Fatalf("existing good dir must be accepted: %v", err)
	}

	wide := filepath.Join(base, "wide")
	if err := os.Mkdir(wide, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckSocketDir(wide, uid); err == nil {
		t.Errorf("mode 0755 must be refused")
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(ok, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckSocketDir(link, uid); err == nil {
		t.Errorf("symlink must be refused")
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSocketDir(file, uid); err == nil {
		t.Errorf("regular file must be refused")
	}

	if err := CheckSocketDir(ok, uid+1); err == nil {
		t.Errorf("a dir owned by another uid must be refused (squatting)")
	}
}

// The formula must match where abduco really binds: create a real session and
// find its socket. Uses a short temp dir so the test itself fits the limit.
func TestSocketPathMatchesAbduco(t *testing.T) {
	if _, err := exec.LookPath("abduco"); err != nil {
		t.Skip("abduco not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "abs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	a, err := New("abduco", filepath.Join(dir, "s"), filepath.Join(dir, "p"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Create("probe-1", dir, []string{"sleep", "30"}, os.Environ()); err != nil {
		t.Fatal(err)
	}
	defer a.Kill("probe-1")
	want := SocketPath(a.SocketDir, a.User(), "probe-1", a.Host())
	if fi, err := os.Lstat(want); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("no socket at the computed path %q (err=%v)", want, err)
	}
}

func TestCreateRefusesOverlongPath(t *testing.T) {
	a := &Adapter{Bin: "abduco-not-run", SocketDir: "/tmp/s", PidDir: t.TempDir(), user: "u", host: "h"}
	long := strings.Repeat("x", SocketPathLimit())
	err := a.Create(long, "/", []string{"true"}, nil)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("Create with an overlong id = %v, want the length error before abduco runs", err)
	}
}

// Squatting defence beyond startup: with CheckDir set, Create re-validates the
// socket dir instead of letting MkdirAll accept whatever now sits at the path.
func TestCreateCheckDirRefusesWideDir(t *testing.T) {
	base := shortTempDir(t) // socket path must fit 103 bytes on macOS
	sock := filepath.Join(base, "s")
	if err := os.Mkdir(sock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sock, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Bin: "abduco-not-run", SocketDir: sock, PidDir: filepath.Join(base, "p"), user: "u", host: "h", CheckDir: true}
	err := a.Create("x-1", "/", []string{"true"}, nil)
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("Create = %v, want the socket-dir mode refusal before abduco runs", err)
	}
}

func TestCreateCheckDirRecreatesMissingDir(t *testing.T) {
	base := shortTempDir(t) // socket path must fit 103 bytes on macOS
	sock := filepath.Join(base, "s")
	a := &Adapter{Bin: "abduco-not-run", SocketDir: sock, PidDir: filepath.Join(base, "p"), user: "u", host: "h", CheckDir: true}
	// abduco-not-run fails to exec, but only after the dir step succeeded.
	if err := a.Create("x-1", "/", []string{"true"}, nil); err == nil {
		t.Fatal("expected the never-run bin to fail")
	}
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatalf("socket dir not recreated: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("recreated socket dir mode = %o, want 700", fi.Mode().Perm())
	}
}

func TestCreateWithoutCheckDirKeepsMkdirAllBehaviour(t *testing.T) {
	base := shortTempDir(t) // socket path must fit 103 bytes on macOS
	sock := filepath.Join(base, "s")
	if err := os.Mkdir(sock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sock, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Bin: "abduco-not-run", SocketDir: sock, PidDir: filepath.Join(base, "p"), user: "u", host: "h"}
	err := a.Create("x-1", "/", []string{"true"}, nil)
	if err == nil || strings.Contains(err.Error(), "mode") {
		t.Fatalf("Create = %v, want a plain exec failure (no dir refusal) when CheckDir is off", err)
	}
}

// Ruling P3: the one startup check serve and doctor share. It must cover the
// longest profile name (in bytes), whatever its position in the list.
func TestCheckProfileSocketPaths(t *testing.T) {
	dir := "/tmp/" + strings.Repeat("d", 40)
	if err := CheckProfileSocketPaths(dir, "alice", "mbp.local", []string{"claude", "zsh"}); err != nil {
		t.Errorf("short names refused: %v", err)
	}
	long := strings.Repeat("phân", 10) // 50 bytes, 40 runes
	err := CheckProfileSocketPaths(dir, "alice", "mbp.local", []string{"claude", long, "zsh"})
	if err == nil || !strings.Contains(err.Error(), long+"-9999") {
		t.Errorf("long profile not refused by name: %v", err)
	}
}
