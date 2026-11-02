package box

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/extraca"
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
	_, _, _, _ = c.InspectMounts(ctx, "x")
	_, _ = c.ListByLabel(ctx, "l")
	_, _ = c.ListBoxes(ctx, "l")
	_ = c.Stop(ctx, "x")
	_ = c.HasRuntime(ctx, "sysbox-runc")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 7 {
		t.Fatalf("want 7 docker calls, got %d: %q", len(lines), lines)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "--host unix:///v/docker.sock --config /v/docker ") {
			t.Fatalf("call without the endpoint flags first: %q", l)
		}
	}
}

func TestListBoxesParsesTheClaimLabels(t *testing.T) {
	out := "ab-box-1\trunning\t/w/a\t2026-10-15 10:00:00 +0000 UTC\tDEV\t1\t1\t.\n" +
		"ab-box-2\trunning\t/w/b\t2026-10-15 10:00:00 +0000 UTC\tPROD\t\t\t.\n" +
		"ab-box-3\texited\t/w/c\t2026-10-14 09:00:00 +0000 UTC\tPROD\t\t1\t."
	bin, log := fakeDocker(t, map[string]string{"ps": out}, nil)
	got, err := CLIClient{Bin: bin}.ListBoxes(context.Background(), LabelInstance)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Instance != "DEV" || !got[0].Exclusive || !got[0].Claims ||
		got[1].Instance != "PROD" || got[1].Exclusive || got[1].Claims || !got[2].Claims || got[2].Exclusive ||
		got[0].Unreadable || got[1].Unreadable || got[2].Unreadable {
		t.Fatalf("parse: %+v", got)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "label="+LabelInstance) || strings.Contains(string(b), "label="+LabelInstance+"=") {
		t.Fatalf("the filter must be the label key alone, so every instance is listed: %s", b)
	}
}

// The listing also returns containers agent-box did not create, whose labels can
// hold tabs and newlines. Such a row must be flagged, never read as clean.
func TestParseBoxListFlagsRowsItCannotSplit(t *testing.T) {
	const created = "2026-10-15 10:00:00 +0000 UTC"
	out := strings.Join([]string{
		"ab-box-tab\trunning\t/w/a\tb\t" + created + "\tPROD\t1\t1\t.", // tab inside the workspace
		"ab-box-nl\trunning\t/w/a",                                     // newline inside the workspace...
		"b\t" + created + "\tPROD\t1\t1\t.",                            // ...and the rest of that row
		"ab-box-old\trunning\t/w/c\t" + created + "\tPROD",             // another format
		"ab-box-ok\trunning\t/w/d\t" + created + "\tPROD\t\t\t.",       // empty labels, last row
	}, "\n")
	got := parseBoxList(strings.TrimSpace(out))
	if len(got) != 5 {
		t.Fatalf("rows: %+v", got)
	}
	for i := 0; i < 4; i++ {
		if !got[i].Unreadable {
			t.Errorf("row %d must be unreadable: %+v", i, got[i])
		}
	}
	if got[1].Name != "ab-box-nl" || got[1].State != "running" {
		t.Errorf("a split row keeps its name and state: %+v", got[1])
	}
	if got[2].State == "running" {
		t.Errorf("the tail of a split row must not read as running: %+v", got[2])
	}
	if got[4].Unreadable || got[4].Workspace != "/w/d" || got[4].Instance != "PROD" {
		t.Errorf("a complete row with empty labels: %+v", got[4])
	}
}

// One docker call answers all three questions ensure asks of a box. The CA
// label is the first line (empty when the box has none, so the line may be
// blank), the running flag the second; only bind mounts count; a stopped or
// missing box reports no mounts, and the stderr rules of Inspect hold unchanged.
func TestInspectMountsReadsStateLabelAndBindsFromOneCall(t *testing.T) {
	cases := []struct {
		name, stdout, errs string
		code               int
		want               State
		mounts             []string
		label              string
		err                bool
	}{
		{"running", "\ntrue\nbind|/w\ntmpfs|/tmp\nbind|/home/agent\nvolume|/v\nbind|/opt/agent-bundle", "", 0,
			StateRunning, []string{"/w", "/home/agent", "/opt/agent-bundle"}, "", false},
		{"running with CA label", "0123456789ab\ntrue\nbind|/w\nbind|/etc/ssl/certs/ca-certificates.crt", "", 0,
			StateRunning, []string{"/w", "/etc/ssl/certs/ca-certificates.crt"}, "0123456789ab", false},
		{"running no mounts", "\ntrue", "", 0, StateRunning, nil, "", false},
		{"stopped", "\nfalse\nbind|/w", "", 0, StateStopped, nil, "", false},
		{"missing", "", "Error: No such object: ab-box-x", 1, StateMissing, nil, "", false},
		{"daemon down", "", "Cannot connect to the Docker daemon", 1, StateMissing, nil, "", true},
		{"warning on stderr", "\ntrue\nbind|/w", "WARNING: a deprecated thing", 0, StateRunning, []string{"/w"}, "", false},
		{"unexpected running line", "\nbind|/w\ntrue", "", 0, StateMissing, nil, "", true},
		{"label line missing", "true\nbind|/w", "", 0, StateMissing, nil, "", true},
	}
	for _, c := range cases {
		bin, log := fakeDockerStderr(t, map[string]string{"inspect": c.stdout}, map[string]string{"inspect": c.errs}, map[string]int{"inspect": c.code})
		st, mounts, label, err := CLIClient{Bin: bin}.InspectMounts(context.Background(), "ab-box-x")
		if st != c.want || (err != nil) != c.err || strings.Join(mounts, ",") != strings.Join(c.mounts, ",") || label != c.label {
			t.Fatalf("%s: got (%v, %v, %q, %v), want (%v, %v, %q, err=%v)", c.name, st, mounts, label, err, c.want, c.mounts, c.label, c.err)
		}
		b, _ := os.ReadFile(log)
		if !strings.Contains(string(b), `{{index .Config.Labels "`+extraca.Label+`"}}`) || !strings.Contains(string(b), "{{.State.Running}}{{range .Mounts}}") {
			t.Fatalf("%s: one inspect must ask for label, state and mounts together: %s", c.name, b)
		}
	}
}

func TestSeccompActive(t *testing.T) {
	cases := []struct {
		name, out string
		code      int
		want      bool
		wantErr   bool
	}{
		{"builtin", `["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]`, 0, true, false},
		{"unconfined", `["name=seccomp,profile=unconfined"]`, 0, false, false},
		{"empty", `[]`, 0, false, false},
		{"null", `null`, 0, false, false},
		{"custom profile", `["name=seccomp,profile=/etc/x.json"]`, 0, true, false},
		{"docker fails", ``, 1, false, true},
		{"not json", `oops`, 0, false, true},
	}
	for _, c := range cases {
		bin, _ := fakeDocker(t, map[string]string{"info": c.out}, map[string]int{"info": c.code})
		got, err := CLIClient{Bin: bin}.SeccompActive(context.Background())
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got (%v, %v), want (%v, err=%v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}

// The in-box identity check compares stdout line by line: a docker CLI warning
// on stderr beside a successful exec must not shift or pollute those lines.
func TestStdoutIgnoresStderrOnSuccess(t *testing.T) {
	bin, _ := fakeDockerStderr(t, map[string]string{"exec": "10:20\n11:21"},
		map[string]string{"exec": "WARNING: the docker CLI is noisy"}, nil)
	c := CLIClient{Bin: bin}
	args := []string{"exec", "ab-box-x", "/usr/bin/stat", "-c", "%d:%i", "--", "/a", "/b"}
	out, err := c.Stdout(context.Background(), args)
	if err != nil || out != "10:20\n11:21" {
		t.Fatalf("Stdout = %q, %v; want the stdout alone", out, err)
	}
	// Run (CombinedOutput) is unchanged for its other callers: it sees both.
	if out, _ := c.Run(context.Background(), args); !strings.Contains(out, "WARNING") {
		t.Fatalf("Run = %q, want it to keep returning combined output", out)
	}
}

func TestStdoutErrorCarriesStderr(t *testing.T) {
	bin, _ := fakeDockerStderr(t, map[string]string{"exec": ""},
		map[string]string{"exec": "stat: cannot statx"}, map[string]int{"exec": 1})
	_, err := CLIClient{Bin: bin}.Stdout(context.Background(), []string{"exec", "ab-box-x", "/usr/bin/stat"})
	if err == nil || !strings.Contains(err.Error(), "cannot statx") {
		t.Fatalf("err = %v, want the stderr text in the error", err)
	}
}
