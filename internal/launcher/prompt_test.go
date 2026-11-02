package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func claudeLauncher(t *testing.T, cfg config.AgentConfig, defs []config.OptionDef) *AgentLauncher {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "c"
	}
	if cfg.Bin == "" {
		cfg.Bin = "/usr/local/bin/claude" // a slash: used verbatim, no PATH lookup
	}
	return New([]config.AgentConfig{cfg}, fakeResolver{}, defs)
}

func TestValidatePrompt(t *testing.T) {
	ok := []string{
		"fix the failing build",
		"line one\nline two",
		"tabs\tare fine",
		"it's $(not) `run` — ünïcode 漢字",
		"--dangerously-skip-permissions please", // a flag-looking prompt is fine: it goes after "--"
		strings.Repeat("a ", maxPromptBytes/2),
	}
	for _, p := range ok {
		if err := ValidatePrompt(p); err != nil {
			t.Errorf("ValidatePrompt(%.30q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		"", "   \n\t ",
		strings.Repeat("a ", maxPromptBytes/2+1),
		"nul\x00byte", "esc \x1b[31m red", "carriage\rreturn", "c1 \u009b csi",
		"doctor", "update", "hi", "fix-it", // one bare word: could be a claude subcommand
		"bad utf8 \xff here",
		"bidi ‮ override", "zero​width", // Unicode format chars (Cf)
	}
	for _, p := range bad {
		if err := ValidatePrompt(p); !errors.Is(err, ErrBadPrompt) {
			t.Errorf("ValidatePrompt(%.30q) = %v, want ErrBadPrompt", p, err)
		}
	}
}

// exactLenPrompt builds a multi-word, no-leading/trailing-whitespace prompt of
// exactly n bytes, so the maxPromptBytes boundary can be pinned exactly
// without the one-bare-word rule or TrimSpace shifting the count.
func exactLenPrompt(n int) string {
	b := make([]byte, n)
	for i := range b {
		if i%2 == 1 {
			b[i] = ' '
		} else {
			b[i] = 'a'
		}
	}
	if n > 0 && b[n-1] == ' ' {
		b[n-1] = 'a'
	}
	return string(b)
}

func TestValidatePromptLengthBoundary(t *testing.T) {
	if err := ValidatePrompt(exactLenPrompt(maxPromptBytes)); err != nil {
		t.Errorf("ValidatePrompt(%d bytes) = %v, want nil (exactly at the cap)", maxPromptBytes, err)
	}
	if err := ValidatePrompt(exactLenPrompt(maxPromptBytes + 1)); !errors.Is(err, ErrBadPrompt) {
		t.Errorf("ValidatePrompt(%d bytes) = %v, want ErrBadPrompt (one over the cap)", maxPromptBytes+1, err)
	}
}

func TestBuildPutsPromptLastAfterDashDash(t *testing.T) {
	defs := []config.OptionDef{{Key: "effort", Type: "enum", Choices: []string{"high", "low"}, Flag: "--effort"}}
	l := claudeLauncher(t, config.AgentConfig{Args: []string{"--add-dir", "/a", "/b"}, Options: map[string]any{"effort": "high"}}, defs)
	argv, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost,
		Resume: "abcd-1234", ExtraArgs: []string{"--strict-mcp-config"}, Prompt: "  -p rm everything  "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/claude", "--add-dir", "/a", "/b", "--effort", "high",
		"--resume", "abcd-1234", "--strict-mcp-config", "--", "-p rm everything"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("argv =\n %q\nwant\n %q", argv, want)
	}
}

func TestBuildWithoutPromptHasNoDashDash(t *testing.T) {
	l := claudeLauncher(t, config.AgentConfig{}, nil)
	argv, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if contains(argv, "--") {
		t.Fatalf("no prompt, but argv carries a --: %v", argv)
	}
}

func TestBuildBoxedPromptIsTheLastArgv(t *testing.T) {
	l := claudeLauncher(t, config.AgentConfig{Bin: "claude"}, nil)
	argv, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeBox,
		ExtraArgs: []string{"--strict-mcp-config"}, Prompt: "run the tests"})
	if err != nil {
		t.Fatal(err)
	}
	n := len(argv)
	if n < 3 || argv[n-3] != "--strict-mcp-config" || argv[n-2] != "--" || argv[n-1] != "run the tests" {
		t.Fatalf("boxed argv tail = %q, want [--strict-mcp-config -- run the tests]", argv)
	}
}

func TestBuildRejectsPromptForNonClaude(t *testing.T) {
	for _, bin := range []string{"/bin/bash", "codex", "/opt/bin/claude-wrapper"} {
		l := New([]config.AgentConfig{{Name: "x", Bin: bin}}, fakeResolver{}, nil)
		if _, _, err := l.Build(SpawnSpec{Agent: "x", Runtime: config.RuntimeBox, Prompt: "do the thing"}); !errors.Is(err, ErrPromptUnsupported) {
			t.Errorf("bin %q: err=%v, want ErrPromptUnsupported", bin, err)
		}
	}
}

func TestBuildRejectsBadPromptBeforeResolvingSecrets(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/local/bin/claude", Secrets: []string{"MISSING"}}}, fakeResolver{}, nil)
	_, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost, Prompt: "doctor"})
	if !errors.Is(err, ErrBadPrompt) {
		t.Fatalf("err=%v, want ErrBadPrompt (checked before the unresolvable secret)", err)
	}
}

func TestBuildResume(t *testing.T) {
	l := claudeLauncher(t, config.AgentConfig{}, nil)
	argv, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost, Resume: "b59c6dcd-11c9-4b0c-86bf-ada6cee780e6"})
	if err != nil || !argvHasPair(argv, "--resume", "b59c6dcd-11c9-4b0c-86bf-ada6cee780e6") {
		t.Fatalf("argv=%v err=%v, want --resume <id>", argv, err)
	}
	for _, bad := range []string{"../x", "a b", "id;rm", strings.Repeat("a", 129), "-p", "--dangerously-skip-permissions"} {
		if _, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost, Resume: bad}); !errors.Is(err, ErrBadResume) {
			t.Errorf("Resume %q: err=%v, want ErrBadResume", bad, err)
		}
	}
	sh := New([]config.AgentConfig{{Name: "s", Bin: "/bin/bash"}}, fakeResolver{}, nil)
	if _, _, err := sh.Build(SpawnSpec{Agent: "s", Runtime: config.RuntimeHost, Resume: "abcd"}); !errors.Is(err, ErrBadResume) {
		t.Errorf("non-claude resume: err=%v, want ErrBadResume", err)
	}
}

// The login wrap is the one place the prompt passes through a shell parser.
// shellJoin must hand it back byte-for-byte, in every shell the daemon may use.
func TestShellJoinRoundTripsHostileText(t *testing.T) {
	dir := t.TempDir()
	pwned := filepath.Join(dir, "pwned")
	hostile := []string{
		"it's a 'quoted' thing",
		"$(touch " + pwned + ")",
		"`touch " + pwned + "`",
		"line1\nline2\n\nline4",
		`a!b "double" \back\slash`,
		"--dangerously-skip-permissions please",
		"tab\there",
		"$HOME ${PATH} %s %d",
	}
	// Production runs zsh as `-l -i -c` (rc files loaded, per argv()'s login
	// wrap). Here we add -f (no rc) so the round trip is deterministic and
	// unaffected by whatever a real ~/.zshrc on this box happens to do; -f
	// changes shell startup behavior, not how -c parses/quotes its argument,
	// so it does not weaken what this test is checking.
	shells := [][]string{{"sh", "-c"}, {"bash", "-c"}, {"zsh", "-f", "-i", "-c"}}
	for _, sh := range shells {
		if _, err := exec.LookPath(sh[0]); err != nil {
			t.Logf("skipping %s: not installed", sh[0])
			continue
		}
		for _, p := range hostile {
			args := append(append([]string{}, sh[1:]...), "printf %s "+shellJoin([]string{p}))
			out, err := exec.Command(sh[0], args...).Output()
			if err != nil {
				t.Fatalf("%s: %v", sh[0], err)
			}
			if string(out) != p {
				t.Errorf("%s round-trip: got %q want %q", sh[0], out, p)
			}
		}
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("a prompt executed a command substitution")
	}
}

func TestBuildLoginPromptIsShellJoinedLast(t *testing.T) {
	l := claudeLauncher(t, config.AgentConfig{Bin: "claude", Base: "login"}, nil)
	l.shell = "/bin/sh"
	p := "it's $(id)\nnext"
	argv, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost, Prompt: p})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(argv[len(argv)-1], " "+shellJoin([]string{"--", p})) {
		t.Fatalf("-c string %q must end with the quoted -- and prompt", argv[len(argv)-1])
	}
}

func TestReplyableFor(t *testing.T) {
	yes, no := true, false
	l := New([]config.AgentConfig{
		{Name: "on", Bin: "claude", Replyable: true},
		{Name: "off", Bin: "claude"},
		{Name: "sh", Bin: "/bin/bash", Replyable: true},
	}, fakeResolver{}, nil)
	cases := []struct {
		spec SpawnSpec
		want bool
	}{
		{SpawnSpec{Agent: "on"}, true},
		{SpawnSpec{Agent: "off"}, false},
		{SpawnSpec{Agent: "on", Replyable: &no}, false},
		{SpawnSpec{Agent: "off", Replyable: &yes}, true},
		{SpawnSpec{Agent: "sh"}, false},                  // replies are claude-only
		{SpawnSpec{Agent: "sh", Replyable: &yes}, false}, // whatever the request says
		{SpawnSpec{Agent: "nope", Replyable: &yes}, false},
	}
	for _, c := range cases {
		if got := l.ReplyableFor(c.spec); got != c.want {
			t.Errorf("ReplyableFor(%+v) = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestAcceptedOptionsKeepsOnlyWhatBuildWouldApply(t *testing.T) {
	defs := []config.OptionDef{
		{Key: "effort", Type: "enum", Choices: []string{"high", "low"}, Flag: "--effort"},
		{Key: "skip", Type: "bool", Flag: "--dangerously-skip-permissions"},
	}
	l := New([]config.AgentConfig{{Name: "c", Bin: "claude", Options: map[string]any{"effort": "high", "skip": false}}}, fakeResolver{}, defs)
	got := l.AcceptedOptions(SpawnSpec{Agent: "c", Options: map[string]any{
		"effort": "low", "skip": true, "model": "opus", // model: not exposed by this profile
	}})
	if len(got) != 2 || got["effort"] != "low" || got["skip"] != true {
		t.Fatalf("AcceptedOptions = %v, want effort=low skip=true only", got)
	}
	if got := l.AcceptedOptions(SpawnSpec{Agent: "c", Options: map[string]any{"effort": "$(evil)"}}); len(got) != 0 {
		t.Fatalf("an invalid value was accepted: %v", got)
	}
}

func dropTestLauncher() *AgentLauncher {
	defs := []config.OptionDef{
		{Key: "effort", Type: "enum", Choices: []string{"high", "low"}, Flag: "--effort"},
		{Key: "skip", Type: "bool", Flag: "--dangerously-skip-permissions"},
		{Key: "model", Type: "enum", Choices: []string{"opus", "sonnet"}, Flag: "--model"},
	}
	return New([]config.AgentConfig{
		{Name: "c", Bin: "claude", Options: map[string]any{"effort": "high", "skip": false}},
		{Name: "bare", Bin: "claude"},
	}, fakeResolver{}, defs)
}

func TestDroppedOptionsUnexposedKey(t *testing.T) {
	l := dropTestLauncher()
	// A bool is a capability (skip = bypass permissions): a profile must opt in.
	got := l.DroppedOptions(SpawnSpec{Agent: "bare", Options: map[string]any{"skip": true}})
	want := []OptionDrop{{Key: "skip", Reason: "this profile does not expose this option"}}
	if !slices.Equal(got, want) {
		t.Fatalf("DroppedOptions = %v, want %v", got, want)
	}
}

// model/effort apply to the bin and default to offer_unset, so they are offered
// on every profile (#35): a profile made in the editor with Model/Effort unset
// still lets a spawn pick them. Values are still checked against the choices,
// and the bin scoping holds. Any other enum stays opt-in (see the next test).
func TestEnumOptionOfferedWithoutProfileDefault(t *testing.T) {
	defs := []config.OptionDef{
		{Key: "model", Type: "enum", Choices: []string{"opus", "sonnet"}, Flag: "--model", Bins: []string{"claude"}},
	}
	l := New([]config.AgentConfig{{Name: "bare", Bin: "claude"}, {Name: "sh", Bin: "bash"}}, fakeResolver{}, defs)
	if got := l.AcceptedOptions(SpawnSpec{Agent: "bare", Options: map[string]any{"model": "sonnet"}}); got["model"] != "sonnet" {
		t.Fatalf("unset enum not offered: %v", got)
	}
	if got := l.DroppedOptions(SpawnSpec{Agent: "bare", Options: map[string]any{"model": "$(evil)"}}); len(got) != 1 || got[0].Reason != "invalid value for this option" {
		t.Fatalf("bad enum value: %v", got)
	}
	if got := l.DroppedOptions(SpawnSpec{Agent: "sh", Options: map[string]any{"model": "sonnet"}}); len(got) != 1 || got[0].Reason != "this profile does not expose this option" {
		t.Fatalf("claude-only enum offered to bash: %v", got)
	}
	// Blank = the agent's own default: nothing reaches the command, nothing is reported.
	if got := l.DroppedOptions(SpawnSpec{Agent: "bare", Options: map[string]any{"model": ""}}); len(got) != 0 {
		t.Fatalf("blank enum reported as dropped: %v", got)
	}
	argv, _, _ := l.Build(SpawnSpec{Agent: "bare", Runtime: config.RuntimeHost, Options: map[string]any{"model": ""}})
	if contains(argv, "--model") {
		t.Fatalf("blank enum reached argv: %v", argv)
	}
}

// An enum other than model/effort can grant a capability: unset on a profile, a
// spawn cannot pick it unless the option opts in with offer_unset (review A-1).
func TestCapabilityEnumNotOfferedUnset(t *testing.T) {
	defs := []config.OptionDef{
		{Key: "permission_mode", Type: "enum", Choices: []string{"default", "bypassPermissions"}, Flag: "--permission-mode", Bins: []string{"claude"}},
	}
	l := New([]config.AgentConfig{{Name: "bare", Bin: "claude"}}, fakeResolver{}, defs)
	spec := SpawnSpec{Agent: "bare", Runtime: config.RuntimeHost, Options: map[string]any{"permission_mode": "bypassPermissions"}}
	if got := l.AcceptedOptions(spec); len(got) != 0 {
		t.Fatalf("capability enum accepted unset: %v", got)
	}
	argv, _, _ := l.Build(spec)
	if contains(argv, "bypassPermissions") {
		t.Fatalf("capability enum reached argv: %v", argv)
	}
}

func TestDroppedOptionsInvalidEnumValue(t *testing.T) {
	l := dropTestLauncher()
	got := l.DroppedOptions(SpawnSpec{Agent: "c", Options: map[string]any{"effort": "$(evil)"}})
	want := []OptionDrop{{Key: "effort", Reason: "invalid value for this option"}}
	if !slices.Equal(got, want) {
		t.Fatalf("DroppedOptions = %v, want %v", got, want)
	}
	for _, d := range got {
		if strings.Contains(d.Reason, "evil") || strings.Contains(d.Key, "evil") {
			t.Fatalf("the rejected value was echoed: %v", d)
		}
	}
	// A wrong type for a bool is invalid too.
	got = l.DroppedOptions(SpawnSpec{Agent: "c", Options: map[string]any{"skip": "yes"}})
	if len(got) != 1 || got[0].Reason != "invalid value for this option" {
		t.Fatalf("bool given a string: %v", got)
	}
}

func TestDroppedOptionsMixOfAcceptedAndDropped(t *testing.T) {
	l := dropTestLauncher()
	got := l.DroppedOptions(SpawnSpec{Agent: "c", Options: map[string]any{
		"effort": "low", "skip": true, "model": "sonnet", "bogus": 1,
	}})
	want := []OptionDrop{
		{Key: "bogus", Reason: "this profile does not expose this option"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DroppedOptions = %v, want %v", got, want)
	}
}

func TestDroppedOptionsNilWhenAllAccepted(t *testing.T) {
	l := dropTestLauncher()
	if got := l.DroppedOptions(SpawnSpec{Agent: "c", Options: map[string]any{"effort": "low", "skip": true}}); got != nil {
		t.Fatalf("DroppedOptions = %v, want nil", got)
	}
	if got := l.DroppedOptions(SpawnSpec{Agent: "c"}); got != nil {
		t.Fatalf("no overrides: %v, want nil", got)
	}
	if got := l.DroppedOptions(SpawnSpec{Agent: "nope", Options: map[string]any{"model": "sonnet"}}); got != nil {
		t.Fatalf("unknown agent: %v, want nil", got)
	}
}

func TestDroppedOptionsSortedAndCapped(t *testing.T) {
	l := dropTestLauncher()
	opts := map[string]any{}
	for i := 0; i < 40; i++ {
		opts[fmt.Sprintf("k%02d", i)] = true
	}
	opts["z"+strings.Repeat("x", 200)] = true
	got := l.DroppedOptions(SpawnSpec{Agent: "bare", Options: opts})
	if len(got) != maxDroppedOptions {
		t.Fatalf("len = %d, want cap %d", len(got), maxDroppedOptions)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Key < got[j].Key }) {
		t.Fatalf("not sorted by key: %v", got)
	}
	if got[0].Key != "k00" {
		t.Fatalf("first = %q, want k00", got[0].Key)
	}
	long := l.DroppedOptions(SpawnSpec{Agent: "bare", Options: map[string]any{"z" + strings.Repeat("x", 200): true}})
	if len(long) != 1 || len(long[0].Key) != maxDroppedKeyBytes {
		t.Fatalf("long key not truncated to %d bytes: %v", maxDroppedKeyBytes, long)
	}
}
