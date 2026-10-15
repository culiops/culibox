package box

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeDocker writes a shell script standing in for docker: it logs its argv and
// prints the canned output for the subcommand (by first non-flag arg).
func fakeDocker(t *testing.T, outputs map[string]string, exitCodes map[string]int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n")
	sb.WriteString("sub=''\nfor a in \"$@\"; do case \"$a\" in --host|--config) skip=1;; *) if [ -n \"$skip\" ]; then skip=''; elif [ -z \"$sub\" ]; then sub=$a; fi;; esac; done\n")
	sb.WriteString("case \"$sub\" in\n")
	for sub, out := range outputs {
		code := exitCodes[sub]
		sb.WriteString("  " + sub + ") printf '%s' '" + out + "'; exit " + strconv.Itoa(code) + ";;\n")
	}
	sb.WriteString("esac\nexit 0\n")
	bin = filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(sb.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestInspectDistinguishesMissingRunningStopped(t *testing.T) {
	cases := []struct {
		out  string
		code int
		want State
		err  bool
	}{
		{"true", 0, StateRunning, false},
		{"false", 0, StateStopped, false},
		{"Error: No such object: ab-box-x", 1, StateMissing, false},
		// Measured 2026-10-15 on the docker CLI of this host: lowercase.
		{"error: no such object: ab-box-x", 1, StateMissing, false},
		{"Cannot connect to the Docker daemon at unix:///x. Is the docker daemon running?", 1, StateMissing, true},
	}
	for _, c := range cases {
		bin, _ := fakeDocker(t, map[string]string{"inspect": c.out}, map[string]int{"inspect": c.code})
		got, err := CLIClient{Bin: bin}.Inspect(context.Background(), "ab-box-x")
		if got != c.want || (err != nil) != c.err {
			t.Fatalf("%q: got (%v, %v), want (%v, err=%v)", c.out, got, err, c.want, c.err)
		}
	}
}

func TestListBoxesParsesNameStateWorkspaceCreated(t *testing.T) {
	out := "ab-box-1\trunning\t/Users/a/proj\t2026-10-15 10:00:00 +0000 UTC\nab-box-2\texited\t/Users/a/other\t2026-10-14 09:00:00 +0000 UTC"
	bin, _ := fakeDocker(t, map[string]string{"ps": out}, nil)
	got, err := CLIClient{Bin: bin}.ListBoxes(context.Background(), LabelInstance)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "ab-box-2" || got[1].State != "exited" ||
		got[0].Workspace != "/Users/a/proj" || got[0].Created.IsZero() {
		t.Fatalf("unexpected parse: %+v", got)
	}
}
