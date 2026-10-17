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
	return fakeDockerStderr(t, outputs, nil, exitCodes)
}

// fakeDockerStderr is fakeDocker with a canned stderr per subcommand, printed
// before the stdout, as docker prints a warning ahead of its answer.
func fakeDockerStderr(t *testing.T, outputs, stderrs map[string]string, exitCodes map[string]int) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n")
	sb.WriteString("sub=''\nfor a in \"$@\"; do case \"$a\" in --host|--config) skip=1;; *) if [ -n \"$skip\" ]; then skip=''; elif [ -z \"$sub\" ]; then sub=$a; fi;; esac; done\n")
	sb.WriteString("case \"$sub\" in\n")
	subs := map[string]bool{}
	for sub := range outputs {
		subs[sub] = true
	}
	for sub := range stderrs {
		subs[sub] = true
	}
	for sub := range subs {
		code := exitCodes[sub]
		sb.WriteString("  " + sub + ") ")
		if e := stderrs[sub]; e != "" {
			sb.WriteString("printf '%s\\n' '" + e + "' >&2; ")
		}
		sb.WriteString("printf '%s' '" + outputs[sub] + "'; exit " + strconv.Itoa(code) + ";;\n")
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
		name         string
		stdout, errs string
		code         int
		want         State
		err          bool
	}{
		{"running", "true", "", 0, StateRunning, false},
		{"stopped", "false", "", 0, StateStopped, false},
		{"missing", "", "Error: No such object: ab-box-x", 1, StateMissing, false},
		// Measured 2026-10-15 on the docker CLI of this host: lowercase.
		{"missing lowercase", "", "error: no such object: ab-box-x", 1, StateMissing, false},
		{"daemon down", "", "Cannot connect to the Docker daemon at unix:///x. Is the docker daemon running?", 1, StateMissing, true},
		// A warning on stderr beside a successful answer must not turn a live
		// box into "stopped": ensure force-removes a stopped one.
		{"running with stderr warning", "true", "WARNING: a deprecated thing", 0, StateRunning, false},
		{"unexpected stdout", "maybe", "", 0, StateMissing, true},
		{"empty stdout", "", "", 0, StateMissing, true},
		{"no such text on stdout only is not missing", "Error: No such object: x", "", 1, StateMissing, true},
	}
	for _, c := range cases {
		bin, _ := fakeDockerStderr(t, map[string]string{"inspect": c.stdout}, map[string]string{"inspect": c.errs}, map[string]int{"inspect": c.code})
		got, err := CLIClient{Bin: bin}.Inspect(context.Background(), "ab-box-x")
		if got != c.want || (err != nil) != c.err {
			t.Fatalf("%s: got (%v, %v), want (%v, err=%v)", c.name, got, err, c.want, c.err)
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

// Every docker call must carry the endpoint flags. The per-session exec runs
// under a curated env that drops DOCKER_HOST/DOCKER_CONTEXT, so an env-based
// endpoint would send management calls and exec calls to different daemons.
func TestEveryClientCallCarriesTheGlobalArgs(t *testing.T) {
	bin, log := fakeDocker(t, map[string]string{"inspect": "true", "info": `{"runc":{}}`}, nil)
	c := CLIClient{Bin: bin, Args: []string{"--host", "unix:///v/docker.sock", "--config", "/v/docker"}}
	ctx := context.Background()
	_, _ = c.Run(ctx, []string{"run", "-d", "x"})
	_, _ = c.Inspect(ctx, "x")
	_, _ = c.ListByLabel(ctx, "l")
	_, _ = c.ListBoxes(ctx, "l")
	_ = c.Stop(ctx, "x")
	_ = c.HasRuntime(ctx, "sysbox-runc")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 6 {
		t.Fatalf("want 6 docker calls, got %d: %q", len(lines), lines)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "--host unix:///v/docker.sock --config /v/docker ") {
			t.Fatalf("call without the endpoint flags first: %q", l)
		}
	}
}

func TestListBoxesParsesTheClaimLabels(t *testing.T) {
	out := "ab-box-1\trunning\t/w/a\t2026-10-15 10:00:00 +0000 UTC\tDEV\t1\t1\n" +
		"ab-box-2\trunning\t/w/b\t2026-10-15 10:00:00 +0000 UTC\tPROD\t\t\n" +
		"ab-box-3\texited\t/w/c\t2026-10-14 09:00:00 +0000 UTC\tPROD\t\t1"
	bin, log := fakeDocker(t, map[string]string{"ps": out}, nil)
	got, err := CLIClient{Bin: bin}.ListBoxes(context.Background(), LabelInstance)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Instance != "DEV" || !got[0].Exclusive || !got[0].Claims ||
		got[1].Instance != "PROD" || got[1].Exclusive || got[1].Claims || !got[2].Claims || got[2].Exclusive {
		t.Fatalf("parse: %+v", got)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "label="+LabelInstance) || strings.Contains(string(b), "label="+LabelInstance+"=") {
		t.Fatalf("the filter must be the label key alone, so every instance is listed: %s", b)
	}
}
