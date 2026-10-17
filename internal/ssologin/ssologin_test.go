package ssologin

import (
	"bufio"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/device-code.txt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.HasPrefix(s, "# stream:") {
		s = s[strings.IndexByte(s, '\n')+1:]
	}
	return s
}

// waitUntil polls cond for up to 2 s and reports whether it held. It never
// touches t, so it is safe from any goroutine.
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// mustWait fails the test (from the test goroutine only) when cond does not hold in time.
func mustWait(t *testing.T, cond func() bool) {
	t.Helper()
	if !waitUntil(cond) {
		t.Fatal("condition not met within 2s")
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// fakeProc stands in for `aws sso login`: it prints output, then blocks in
// Wait until exit(err) or Kill().
type fakeProc struct {
	args      []string
	pr        *io.PipeReader
	pw        *io.PipeWriter
	written   chan struct{}
	exitCh    chan error
	killCh    chan struct{}
	killOnce  sync.Once
	mu        sync.Mutex
	wasKilled bool
}

func (p *fakeProc) Output() io.Reader { return p.pr }

func (p *fakeProc) Wait() error {
	var err error
	select {
	case err = <-p.exitCh:
	case <-p.killCh:
		err = errors.New("signal: killed")
	}
	<-p.written
	p.pw.Close()
	return err
}

func (p *fakeProc) Kill() {
	p.mu.Lock()
	p.wasKilled = true
	p.mu.Unlock()
	p.killOnce.Do(func() { close(p.killCh) })
}

func (p *fakeProc) killed() bool   { p.mu.Lock(); defer p.mu.Unlock(); return p.wasKilled }
func (p *fakeProc) exit(err error) { p.exitCh <- err }

type fakeStarter struct {
	mu     sync.Mutex
	output string
	procs  []*fakeProc
}

func (fs *fakeStarter) start(_ string, args, _ []string) (proc, error) {
	pr, pw := io.Pipe()
	p := &fakeProc{args: args, pr: pr, pw: pw, written: make(chan struct{}),
		exitCh: make(chan error, 1), killCh: make(chan struct{})}
	out := fs.output
	go func() { defer close(p.written); _, _ = pw.Write([]byte(out)) }()
	fs.mu.Lock()
	fs.procs = append(fs.procs, p)
	fs.mu.Unlock()
	return p, nil
}

func (fs *fakeStarter) starts() int { fs.mu.Lock(); defer fs.mu.Unlock(); return len(fs.procs) }
func (fs *fakeStarter) last() *fakeProc {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.procs[len(fs.procs)-1]
}
func (fs *fakeStarter) all() []*fakeProc {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return slices.Clone(fs.procs)
}

func newFakeRunner(t *testing.T) (*Runner, *fakeStarter) {
	t.Helper()
	fs := &fakeStarter{output: fixture(t)}
	r := New("/usr/bin/aws", nil)
	r.start = fs.start
	r.parseTimeout = 200 * time.Millisecond
	t.Cleanup(r.Close)
	return r, fs
}

func TestParseTheCapturedFixture(t *testing.T) {
	uri, code, ok := Parse(fixture(t))
	if !ok || code != "ABCD-EFGH" || !TrustedURI(uri) || !strings.Contains(uri, "user_code=ABCD-EFGH") {
		t.Fatalf("Parse(fixture) = %q %q %v", uri, code, ok)
	}
}

func TestParseNeedsAURLAndACode(t *testing.T) {
	for _, s := range []string{"", "Please visit https://device.sso.ap-southeast-1.amazonaws.com/\n",
		"ABCD-EFGH\n", "abcd-efgh https://x\n", "code: ABCD-EFGHI https://x\n"} {
		if _, _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) ok", s)
		}
	}
}

func TestParsePrefersTheAutofillURL(t *testing.T) {
	s := "https://device.sso.ap-southeast-1.amazonaws.com/\n\nABCD-EFGH\n\nhttps://device.sso.ap-southeast-1.amazonaws.com/?user_code=ABCD-EFGH\n"
	if uri, _, _ := Parse(s); !strings.Contains(uri, "user_code=ABCD-EFGH") {
		t.Fatalf("got %q", uri)
	}
}

func TestTrustedURI(t *testing.T) {
	good := []string{
		"https://device.sso.ap-southeast-1.amazonaws.com/",
		"https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH",
		"https://d-0000000000.awsapps.com/start/#/device?user_code=ABCD-EFGH",
	}
	bad := []string{
		"http://device.sso.ap-southeast-1.amazonaws.com/",
		"https://device.sso.ap-southeast-1.amazonaws.com.evil.example/",
		"https://evil.example/?u=device.sso.ap-southeast-1.amazonaws.com",
		"https://awsapps.com/", "https://.awsapps.com/", "https://x.awsapps.com:444/",
		"https://user@d-0000000000.awsapps.com/", "https://device.sso.AP-1.amazonaws.com/",
		"javascript:alert(1)", "https://device.sso.ap-southeast-1.amazonaws.com\\@evil.example/",
		"https://a.b.awsapps.com/", "https://evil.example/.awsapps.com",
	}
	for _, u := range good {
		if !TrustedURI(u) {
			t.Errorf("refused %q", u)
		}
	}
	for _, u := range bad {
		if TrustedURI(u) {
			t.Errorf("trusted %q", u)
		}
	}
}

func TestArgsAreFixedAndTheProfileIsChecked(t *testing.T) {
	want := []string{"sso", "login", "--profile", "sandbox-admin", "--use-device-code", "--no-browser"}
	if !slices.Equal(Args("sandbox-admin"), want) {
		t.Fatalf("Args = %q", Args("sandbox-admin"))
	}
	r, fs := newFakeRunner(t)
	for _, p := range []string{"", "a b", "a;b", "--debug", strings.Repeat("x", 65)} {
		if _, err := r.Start("k", p, func(bool, string) {}); err == nil {
			t.Errorf("profile %q accepted", p)
		}
	}
	if fs.starts() != 0 {
		t.Fatal("a refused profile must not start a process")
	}
}

func TestCuratedEnv(t *testing.T) {
	in := []string{"HOME=/h", "PATH=/p", "CLAUDE_CODE_OAUTH_TOKEN=secret", "AB_PROD_PASS=secret", "AWS_PROFILE=other",
		"AWS_CONFIG_FILE=/c", "LANG=C.UTF-8", "HTTPS_PROXY=http://p:3128", "AWS_ACCESS_KEY_ID=AKIA"}
	got := curatedEnv(in)
	want := []string{"HOME=/h", "PATH=/p", "AWS_CONFIG_FILE=/c", "LANG=C.UTF-8", "HTTPS_PROXY=http://p:3128"}
	if !slices.Equal(got, want) {
		t.Fatalf("curatedEnv = %q", got)
	}
}

func TestStartReturnsTheCodeThenCompletes(t *testing.T) {
	r, fs := newFakeRunner(t)
	var calls []bool
	var mu sync.Mutex
	st, err := r.Start("culi-sso", "sandbox-admin", func(ok bool, _ string) { mu.Lock(); calls = append(calls, ok); mu.Unlock() })
	if err != nil || st.State != StateWaiting || st.Code != "ABCD-EFGH" || !st.URITrusted || st.Expires.IsZero() ||
		!strings.Contains(st.URI, "user_code=ABCD-EFGH") {
		t.Fatalf("Start = %+v %v", st, err)
	}
	if got := fs.last().args; !slices.Equal(got, Args("sandbox-admin")) {
		t.Fatalf("argv %q", got)
	}
	fs.last().exit(nil)
	mustWait(t, func() bool { s, _ := r.Status("culi-sso"); return s.State == StateOK })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(calls, []bool{true}) {
		t.Fatalf("done calls %v", calls)
	}
}

func TestSingleFlightPerKey(t *testing.T) {
	r, fs := newFakeRunner(t)
	a, _ := r.Start("culi-sso", "sandbox-admin", func(bool, string) {})
	b, _ := r.Start("culi-sso", "sandbox-ro", func(bool, string) {})
	if fs.starts() != 1 || a.Code != b.Code {
		t.Fatalf("a second click must return the running flow: %d starts", fs.starts())
	}
	r.Start("corp-sso", "company", func(bool, string) {})
	if fs.starts() != 2 {
		t.Fatal("another sso session gets its own flow")
	}
}

func TestUnparsedOutputKillsAndFailsOnce(t *testing.T) {
	r, fs := newFakeRunner(t)
	fs.output = "Something unexpected\n"
	var mu sync.Mutex
	var n int
	var detail string
	_, err := r.Start("culi-sso", "sandbox-admin", func(ok bool, d string) { mu.Lock(); n++; detail = d; mu.Unlock() })
	if !errors.Is(err, ErrUnparsed) || !fs.last().killed() {
		t.Fatalf("err %v killed %v", err, fs.last().killed())
	}
	mustWait(t, func() bool { s, _ := r.Status("culi-sso"); return s.State == StateFailed })
	mu.Lock()
	defer mu.Unlock()
	if n != 1 || detail != "unparsed output" {
		t.Fatalf("done called %d times, detail %q", n, detail)
	}
}

func TestAnExitWithoutACodeReportsTheExitStatus(t *testing.T) {
	bin := t.TempDir() + "/aws"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'The config profile could not be found'\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := New(bin, nil)
	t.Cleanup(r.Close)
	var mu sync.Mutex
	var detail string
	_, err := r.Start("k", "nope", func(ok bool, d string) { mu.Lock(); detail = d; mu.Unlock() })
	if !errors.Is(err, ErrUnparsed) {
		t.Fatalf("err %v", err)
	}
	mustWait(t, func() bool { s, _ := r.Status("k"); return s.State == StateFailed })
	mu.Lock()
	defer mu.Unlock()
	if detail != "exit status 3" {
		t.Fatalf("detail %q", detail)
	}
}

func TestTheFlowIsCappedAndKilled(t *testing.T) {
	r, fs := newFakeRunner(t)
	r.flowTimeout = 80 * time.Millisecond
	var mu sync.Mutex
	var detail string
	r.Start("culi-sso", "sandbox-admin", func(ok bool, d string) { mu.Lock(); detail = d; mu.Unlock() })
	mustWait(t, func() bool { return fs.last().killed() })
	mustWait(t, func() bool { s, _ := r.Status("culi-sso"); return s.State == StateFailed })
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(detail, "timed out") {
		t.Fatalf("detail %q", detail)
	}
}

func TestFinishedFlowsAreForgotten(t *testing.T) {
	r, fs := newFakeRunner(t)
	r.keepDone = 30 * time.Millisecond
	r.Start("culi-sso", "sandbox-admin", func(bool, string) {})
	fs.last().exit(nil)
	mustWait(t, func() bool { s, _ := r.Status("culi-sso"); return s.State == StateOK })
	time.Sleep(60 * time.Millisecond)
	if _, ok := r.Status("culi-sso"); ok {
		t.Fatal("a finished flow outlived keepDone")
	}
}

func TestCloseKillsEveryFlowAndLeaksNothing(t *testing.T) {
	base := runtime.NumGoroutine()
	r, fs := newFakeRunner(t)
	var mu sync.Mutex
	var details []string
	done := func(_ bool, d string) { mu.Lock(); details = append(details, d); mu.Unlock() }
	r.Start("a", "p1", done)
	r.Start("b", "p2", done)
	r.Close()
	for _, p := range fs.all() {
		if !p.killed() {
			t.Fatal("Close left a process")
		}
	}
	mu.Lock()
	if !slices.Equal(details, []string{"daemon shutting down", "daemon shutting down"}) {
		t.Fatalf("details %q", details)
	}
	mu.Unlock()
	if _, err := r.Start("c", "p3", func(bool, string) {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close: %v", err)
	}
	mustWait(t, func() bool { return runtime.NumGoroutine() <= base })
}

// The real starter kills the whole group, so a child the CLI forks dies too.
func TestRealProcessGroupIsKilled(t *testing.T) {
	pidFile := t.TempDir() + "/child"
	script := "printf 'https://device.sso.ap-southeast-1.amazonaws.com/\\nABCD-EFGH\\n'; sleep 60 & echo $! > " + pidFile + "; wait"
	p, err := realStart("/bin/sh", []string{"-c", script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Read lines until the code shows: the script prints little and then
	// `sleep` keeps the pipe open, so a fixed-size read would block.
	var seen strings.Builder
	sc := bufio.NewScanner(p.Output())
	found := false
	for sc.Scan() {
		seen.WriteString(sc.Text() + "\n")
		if strings.Contains(sc.Text(), "ABCD-EFGH") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("output %q", seen.String())
	}
	mustWait(t, func() bool { _, err := os.Stat(pidFile); return err == nil })
	var child int
	mustWait(t, func() bool {
		raw, _ := os.ReadFile(pidFile)
		child = atoi(strings.TrimSpace(string(raw)))
		return child > 0
	})
	p.Kill()
	_ = p.Wait()
	mustWait(t, func() bool { return syscall.Kill(child, 0) == syscall.ESRCH })
}

// End to end through the real starter with a stand-in binary, never the real aws.
func TestRunnerWithARealProcess(t *testing.T) {
	bin := t.TempDir() + "/aws"
	script := "#!/bin/sh\ncat " + abs(t, "testdata/device-code.txt") + " | tail -n +2\nsleep 0.2\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r := New(bin, []string{"PATH=/usr/bin:/bin", "SECRET=x"})
	t.Cleanup(r.Close)
	st, err := r.Start("k", "sandbox-admin", func(bool, string) {})
	if err != nil || st.Code != "ABCD-EFGH" || !st.URITrusted {
		t.Fatalf("Start = %+v %v", st, err)
	}
	mustWait(t, func() bool { s, _ := r.Status("k"); return s.State == StateOK })
}

func abs(t *testing.T, rel string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd + "/" + rel
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir() + "/aws"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A CLI that closes stdout without a code but keeps running must not hold the
// key hostage: the group is killed, the flow fails promptly, a retry works.
func TestSilentStdoutCloseKillsAndAllowsARetry(t *testing.T) {
	counter := t.TempDir() + "/runs"
	bin := writeScript(t, "echo x >> "+counter+"\nexec 1>&-\nsleep 60\n")
	r := New(bin, []string{"PATH=/usr/bin:/bin"})
	r.exitGrace = 50 * time.Millisecond
	t.Cleanup(r.Close)
	var mu sync.Mutex
	var details []string
	done := func(_ bool, d string) { mu.Lock(); details = append(details, d); mu.Unlock() }
	begin := time.Now()
	if _, err := r.Start("k", "p", done); !errors.Is(err, ErrUnparsed) {
		t.Fatalf("err %v", err)
	}
	if s, _ := r.Status("k"); s.State != StateFailed {
		t.Fatalf("state %q right after the failed Start", s.State)
	}
	if time.Since(begin) > 3*time.Second {
		t.Fatal("the failure was not prompt")
	}
	mu.Lock()
	if !slices.Equal(details, []string{"unparsed output"}) {
		t.Fatalf("details %q", details)
	}
	mu.Unlock()
	if _, err := r.Start("k", "p", done); !errors.Is(err, ErrUnparsed) {
		t.Fatalf("retry err %v", err)
	}
	raw, _ := os.ReadFile(counter)
	if n := strings.Count(string(raw), "x"); n != 2 {
		t.Fatalf("the retry must start a new process, runs = %d", n)
	}
}

// A grandchild the CLI leaves behind dies when the leader exits normally.
func TestAGrandchildDoesNotOutliveTheLeader(t *testing.T) {
	pidFile := t.TempDir() + "/child"
	p, err := realStart("/bin/sh", []string{"-c", "sleep 60 & echo $! > " + pidFile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Wait()
	var child int
	mustWait(t, func() bool {
		raw, _ := os.ReadFile(pidFile)
		child = atoi(strings.TrimSpace(string(raw)))
		return child > 0
	})
	mustWait(t, func() bool { return syscall.Kill(child, 0) == syscall.ESRCH })
}

func TestAHugeLineDoesNotStallTheReader(t *testing.T) {
	huge := strings.Repeat("a", 100_000) + "\n"
	for name, tc := range map[string]struct {
		output string
		parsed bool
	}{
		"code first":  {fixture(t) + huge, true},
		"code buried": {huge + fixture(t), false},
	} {
		t.Run(name, func(t *testing.T) {
			r, fs := newFakeRunner(t)
			fs.output = tc.output
			st, err := r.Start("k", "p", func(bool, string) {})
			if tc.parsed {
				if err != nil || st.Code != "ABCD-EFGH" {
					t.Fatalf("Start = %+v %v", st, err)
				}
				fs.last().exit(nil)
			} else if !errors.Is(err, ErrUnparsed) {
				t.Fatalf("err %v", err)
			}
			// Either way the whole output was drained and the flow ends.
			mustWait(t, func() bool { s, _ := r.Status("k"); return s.State != StateWaiting })
		})
	}
}

func TestALastLineWithoutANewline(t *testing.T) {
	r, fs := newFakeRunner(t)
	fs.output = strings.TrimSuffix(fixture(t), "\n") // the autofill URL has no newline
	st, err := r.Start("k", "p", func(bool, string) {})
	if err != nil || st.Code != "ABCD-EFGH" || !st.URITrusted {
		t.Fatalf("Start = %+v %v", st, err)
	}
	fs.last().exit(nil) // EOF: the unterminated line is parsed at the end
	mustWait(t, func() bool {
		s, _ := r.Status("k")
		return s.State == StateOK && strings.Contains(s.URI, "user_code=ABCD-EFGH")
	})
}

func TestACodeLineWithoutANewlineIsReadAtEOF(t *testing.T) {
	r, fs := newFakeRunner(t)
	fs.output = "https://d-0000000000.awsapps.com/start/#/device\nABCD-EFGH"
	go func() {
		if waitUntil(func() bool { return fs.starts() == 1 }) { // on a miss Start fails the test itself
			fs.last().exit(nil)
		}
	}()
	st, err := r.Start("k", "p", func(bool, string) {})
	if err != nil || st.Code != "ABCD-EFGH" {
		t.Fatalf("Start = %+v %v", st, err)
	}
}

// The CLI sees only the curated environment: the daemon's secrets are absent.
func TestChildEnvironmentIsCurated(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-from-the-daemon")
	t.Setenv("AB_PROD_PASS", "pw-from-the-daemon")
	envFile := t.TempDir() + "/env"
	bin := writeScript(t, "env > "+envFile+"\ncat "+abs(t, "testdata/device-code.txt")+" | tail -n +2\n")
	r := New(bin, os.Environ())
	t.Cleanup(r.Close)
	if _, err := r.Start("k", "p", func(bool, string) {}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, bad := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "AB_PROD_PASS", "tok-from-the-daemon", "pw-from-the-daemon"} {
		if strings.Contains(got, bad) {
			t.Errorf("child environment leaks %s", bad)
		}
	}
	if !strings.Contains(got, "PATH=") {
		t.Error("child lost PATH")
	}
}

func TestConcurrentStartsShareOneProcess(t *testing.T) {
	r, fs := newFakeRunner(t)
	var wg sync.WaitGroup
	states := make([]State, 8)
	errs := make([]error, 8)
	for i := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i], errs[i] = r.Start("k", "p", func(bool, string) {})
		}()
	}
	wg.Wait()
	if fs.starts() != 1 {
		t.Fatalf("%d processes started", fs.starts())
	}
	for i := range states {
		if errs[i] != nil || states[i].Code != "ABCD-EFGH" {
			t.Fatalf("start %d = %+v %v", i, states[i], errs[i])
		}
	}
}
