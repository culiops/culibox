package boximage

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHashIsStableAndShort(t *testing.T) {
	if Hash() != Hash() || len(Hash()) != 12 {
		t.Fatalf("Hash() = %q", Hash())
	}
}

func TestContextTarHoldsOnlyTheDockerfile(t *testing.T) {
	b, err := contextTar()
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(b))
	h, err := tr.Next()
	if err != nil || h.Name != "Dockerfile" {
		t.Fatalf("first entry: %v %v", h, err)
	}
	body, _ := io.ReadAll(tr)
	if !bytes.Equal(body, dockerfile) {
		t.Fatal("tar body differs from the embedded Dockerfile")
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatal("context must contain only the Dockerfile")
	}
}

func fakeDocker(t *testing.T, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "docker")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" + script
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// A user's local node:* tag can be any arch (spike run 2): a plain build
// silently produced an emulated box. --pull and --platform are mandatory.
func TestBuildPinsPlatformPullsAndLabels(t *testing.T) {
	bin, log := fakeDocker(t, `case "$*" in *"version"*) echo arm64;; *) cat >/dev/null;; esac`+"\n")
	err := Docker{Bin: bin, Args: []string{"--host", "unix:///s"}}.Build(context.Background(), []string{"culiops/agent-box:base"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	for _, want := range []string{"--host unix:///s build", "--pull", "--platform linux/arm64",
		"--label " + LabelHash + "=" + Hash(), "-t culiops/agent-box:base -"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build argv missing %q: %s", want, got)
		}
	}
}

func TestImageCheck(t *testing.T) {
	cases := []struct {
		s     ImageState
		fatal bool
		msg   string
	}{
		{ImageState{}, true, "box setup"},
		{ImageState{Present: true, Arch: "amd64", Hash: Hash()}, true, "amd64"},
		{ImageState{Present: true, Arch: "arm64", Hash: "old"}, false, "-rebuild"},
		{ImageState{Present: true, Arch: "arm64", Hash: Hash()}, false, ""},
	}
	for _, c := range cases {
		fatal, msg := c.s.Check("arm64")
		if fatal != c.fatal || !strings.Contains(msg, c.msg) || (c.msg == "" && msg != "") {
			t.Fatalf("%+v: got (%v, %q)", c.s, fatal, msg)
		}
	}
}

func TestInspectMissingImage(t *testing.T) {
	bin, _ := fakeDocker(t, "echo 'Error: No such image: x' >&2; exit 1\n")
	st, err := Docker{Bin: bin}.Inspect(context.Background(), "x")
	if err != nil || st.Present {
		t.Fatalf("missing image: %+v %v", st, err)
	}
}

// A CLI or context warning on stderr must not become part of the answer: read
// into the arch it made every darwin spawn fail as "wrong arch".
func TestInspectIgnoresAWarningOnStderr(t *testing.T) {
	bin, _ := fakeDocker(t, "echo 'WARNING: some context warning' >&2; echo 'arm64|"+Hash()+"'\n")
	st, err := Docker{Bin: bin}.Inspect(context.Background(), "x")
	if err != nil || !st.Present || st.Arch != "arm64" || st.Hash != Hash() {
		t.Fatalf("a stderr warning corrupted the answer: %+v %v", st, err)
	}
	if fatal, msg := st.Check("arm64"); fatal {
		t.Fatalf("a current image read as fatal: %s", msg)
	}
}

// Other failures stay errors and carry docker's stderr.
func TestInspectFailureIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, "echo 'Cannot connect to the Docker daemon' >&2; exit 1\n")
	if _, err := (Docker{Bin: bin}).Inspect(context.Background(), "x"); err == nil ||
		!strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("want an error carrying stderr, got %v", err)
	}
}

// Every name goes on one build, so the dated tag and :base are the same image.
func TestBuildTagsEveryName(t *testing.T) {
	bin, log := fakeDocker(t, `case "$*" in *"version"*) echo arm64;; *) cat >/dev/null;; esac`+"\n")
	d := Docker{Bin: bin}
	if err := d.Build(context.Background(), []string{"r:2026-10-18", "r:base"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "-t r:2026-10-18 -t r:base -") {
		t.Fatalf("build argv: %s", b)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := d.Build(context.Background(), nil, io.Discard); err == nil {
		t.Fatal("a build with no tags must be an error")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("docker must not run for a build with no tags")
	}
}

func TestIsDated(t *testing.T) {
	digest := "x@sha256:" + strings.Repeat("a", 64)
	for ref, want := range map[string]bool{
		"culiops/agent-box:2026-10-18":    true,
		"culiops/agent-box:2026-10-18.2":  true,
		"reg:5000/a/b:2026-10-18":         true,
		"culiops/agent-box:base":          false,
		"culiops/agent-box:2026-10-18.10": false,
		"culiops/agent-box":               false,
		digest:                            false,
		"reg:5000/a":                      false,
	} {
		if got := IsDated(ref); got != want {
			t.Errorf("IsDated(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestRepo(t *testing.T) {
	for ref, want := range map[string]string{
		"reg:5000/a/b:base":            "reg:5000/a/b",
		"a/b":                          "a/b",
		"culiops/agent-box:2026-10-18": "culiops/agent-box",
	} {
		got, err := Repo(ref)
		if err != nil || got != want {
			t.Errorf("Repo(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := Repo("x@sha256:" + strings.Repeat("a", 64)); err == nil {
		t.Error("a digest ref has no repo+tag split; want an error")
	}
}

// presentScript answers `image inspect <ref>` as present for the listed refs
// and as missing for any other.
func presentScript(refs ...string) string {
	s := "last=\"\"; for a; do last=$a; done\ncase \"$last\" in\n"
	for _, r := range refs {
		s += "  " + r + ") echo 'arm64|x'; exit 0;;\n"
	}
	return s + "  *) echo 'Error: No such image: '$last >&2; exit 1;;\nesac\n"
}

func TestNextDatedTag(t *testing.T) {
	ny := time.FixedZone("EST", -5*3600)
	day := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	all := []string{"r:2026-10-18"}
	for _, n := range "23456789" {
		all = append(all, "r:2026-10-18."+string(n))
	}
	cases := []struct {
		name    string
		present []string
		now     time.Time
		want    string
		err     bool
	}{
		{"none exist", nil, day, "r:2026-10-18", false},
		{"base taken", []string{"r:2026-10-18"}, day, "r:2026-10-18.2", false},
		{"two taken", []string{"r:2026-10-18", "r:2026-10-18.2"}, day, "r:2026-10-18.3", false},
		{"all taken", all, day, "", true},
		{"UTC date, not local", nil, time.Date(2026, 10, 18, 23, 30, 0, 0, ny), "r:2026-10-19", false},
	}
	for _, c := range cases {
		bin, _ := fakeDocker(t, presentScript(c.present...))
		got, err := Docker{Bin: bin}.NextDatedTag(context.Background(), "r", c.now)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: got %q, %v; want %q (err %v)", c.name, got, err, c.want, c.err)
		}
	}
}

func TestCheckForPinned(t *testing.T) {
	const pin, base = "culiops/agent-box:2026-10-18", "culiops/agent-box:base"
	fatal, msg := ImageState{}.CheckFor(pin, "arm64")
	if !fatal || !strings.Contains(msg, "never rebuilds or pulls") || !strings.Contains(msg, pin) {
		t.Fatalf("missing pin: (%v, %q)", fatal, msg)
	}
	stale := ImageState{Present: true, Arch: "arm64", Hash: "old"}
	fatal, msg = stale.CheckFor(pin, "arm64")
	if fatal || !strings.Contains(msg, "pinned to "+pin) {
		t.Fatalf("stale pin: (%v, %q)", fatal, msg)
	}
	if fatal, msg = stale.CheckFor(base, "arm64"); fatal || !strings.Contains(msg, "-rebuild") || strings.Contains(msg, "pinned") {
		t.Fatalf("stale unpinned: (%v, %q)", fatal, msg)
	}
	if fatal, _ = (ImageState{Present: true, Arch: "amd64", Hash: Hash()}).CheckFor(pin, "arm64"); !fatal {
		t.Fatal("a wrong-arch pin is still fatal")
	}
	if fatal, msg = (ImageState{Present: true, Arch: "arm64", Hash: Hash()}).CheckFor(pin, "arm64"); fatal || msg != "" {
		t.Fatalf("current pin: (%v, %q)", fatal, msg)
	}
}
