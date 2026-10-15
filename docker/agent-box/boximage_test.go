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
	err := Docker{Bin: bin, Args: []string{"--host", "unix:///s"}}.Build(context.Background(), "culiops/agent-box:base", io.Discard)
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
