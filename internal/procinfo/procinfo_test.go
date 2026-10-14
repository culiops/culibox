package procinfo

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// procargs2 builds a darwin KERN_PROCARGS2 buffer: int32 argc, exec path, NUL
// padding, argv (NUL-terminated each), envp (NUL-terminated each), an empty
// string, then the kernel's "apple" strings.
func procargs2(argc int32, exe string, pad int, args, env, apple []string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, argc)
	b.WriteString(exe)
	b.Write(make([]byte, 1+pad))
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte(0)
	}
	for _, e := range env {
		b.WriteString(e)
		b.WriteByte(0)
	}
	b.WriteByte(0)
	for _, s := range apple {
		b.WriteString(s)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func TestParseProcArgs2(t *testing.T) {
	buf := procargs2(3, "/opt/homebrew/bin/abduco", 5,
		[]string{"abduco", "-a", "claude-1"},
		[]string{"PATH=/usr/bin", "ABDUCO_SOCKET_DIR=/tmp/agent-box-501-1a2b3c4d"},
		[]string{"executable_path=/opt/homebrew/bin/abduco"})
	args, env, err := parseProcArgs2(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, []string{"abduco", "-a", "claude-1"}) {
		t.Errorf("args = %q", args)
	}
	if !slices.Equal(env, []string{"PATH=/usr/bin", "ABDUCO_SOCKET_DIR=/tmp/agent-box-501-1a2b3c4d"}) {
		t.Errorf("env = %q (apple strings must not leak into env)", env)
	}
}

func TestParseProcArgs2_Malformed(t *testing.T) {
	good := procargs2(2, "/bin/x", 0, []string{"x", "y"}, nil, nil)
	cases := map[string][]byte{
		"empty":          nil,
		"short":          {1, 0},
		"negative argc":  procargs2(-1, "/bin/x", 0, nil, nil, nil),
		"argc too large": procargs2(1<<20, "/bin/x", 0, []string{"x"}, nil, nil),
		"no exec NUL":    append([]byte{1, 0, 0, 0}, []byte("/bin/x")...),
		"truncated argv": good[:len(good)-4],
	}
	for name, b := range cases {
		if _, _, err := parseProcArgs2(b); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestParseProcArgs2_EnvRunsToEnd(t *testing.T) {
	// No empty-string terminator after env: return what is there, never panic.
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, int32(1))
	b.WriteString("/bin/x\x00x\x00A=1\x00B=2\x00")
	args, env, err := parseProcArgs2(b.Bytes())
	if err != nil || !slices.Equal(args, []string{"x"}) || !slices.Equal(env, []string{"A=1", "B=2"}) {
		t.Fatalf("args=%q env=%q err=%v", args, env, err)
	}
}

func TestStatField(t *testing.T) {
	// comm may contain spaces and ')': fields are counted after the LAST ')'.
	stat := "1234 (a) b) S 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 99999 23 24"
	if f, ok := statField(stat, 4); !ok || f != "4" {
		t.Errorf("field 4 = %q,%v want 4", f, ok)
	}
	if f, ok := statField(stat, 22); !ok || f != "99999" {
		t.Errorf("field 22 = %q,%v want 99999", f, ok)
	}
	for _, bad := range []string{"", "no paren 1 2 3", "1 (x) S 4"} {
		if _, ok := statField(bad, 22); ok {
			t.Errorf("statField(%q, 22) ok=true, want false", bad)
		}
	}
}

func TestSplitNul(t *testing.T) {
	if got := splitNul([]byte("a\x00\x00b\x00")); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("splitNul = %q", got)
	}
}

// Live probes: these run on Linux here and on darwin on the Mac — they are the
// darwin implementation's only real test.
func TestLiveSelf(t *testing.T) {
	self := os.Getpid()
	if p, err := Ppid(self); err != nil || p != os.Getppid() {
		t.Errorf("Ppid(self) = %d,%v want %d", p, err, os.Getppid())
	}
	if s, err := StartTime(self); err != nil || s == "" {
		t.Errorf("StartTime(self) = %q,%v", s, err)
	}
	if a, err := Args(self); err != nil || len(a) == 0 || a[0] != os.Args[0] {
		t.Errorf("Args(self) = %q,%v want argv[0]=%q", a, err, os.Args[0])
	}
	env, err := Env(self)
	if err != nil || !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "PATH=") }) {
		t.Errorf("Env(self) has no PATH= (err=%v, %d entries)", err, len(env))
	}
	pids, err := PIDs()
	if err != nil || !slices.Contains(pids, self) {
		t.Errorf("PIDs() missing self (err=%v, %d pids)", err, len(pids))
	}
}

func TestLiveStartTimeStable(t *testing.T) {
	a, _ := StartTime(os.Getpid())
	b, _ := StartTime(os.Getpid())
	if a != b {
		t.Errorf("StartTime not stable: %q then %q", a, b)
	}
}

func TestLiveGonePid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("no `true` binary")
	}
	pid := cmd.Process.Pid // exited and reaped
	if _, err := Ppid(pid); err == nil {
		t.Errorf("Ppid(reaped %d) = nil error, want error", pid)
	}
	if _, err := StartTime(pid); err == nil {
		t.Errorf("StartTime(reaped %d) = nil error, want error", pid)
	}
}
