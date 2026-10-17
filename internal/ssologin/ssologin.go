// Package ssologin runs the AWS device-code sign-in (`aws sso login
// --use-device-code --no-browser`) on behalf of the human at the UI. The
// process output is never logged, stored or returned: only the parsed URL and
// code are kept, in memory, for the flow's lifetime.
package ssologin

import (
	"errors"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	StateWaiting = "waiting"
	StateOK      = "ok"
	StateFailed  = "failed"
)

type State struct {
	State      string    `json:"state"`
	URI        string    `json:"uri,omitempty"`
	URITrusted bool      `json:"uri_trusted"`
	Code       string    `json:"code,omitempty"`
	Expires    time.Time `json:"expires,omitzero"`
}

// Done is called exactly once per flow; detail is a fixed phrase or "exit status N".
type Done func(ok bool, detail string)

var (
	ErrUnparsed   = errors.New("could not read the sign-in code from aws sso login")
	ErrClosed     = errors.New("sign-in runner is shut down")
	errBadProfile = errors.New("invalid profile name")
)

const (
	maxOutput        = 64 << 10
	reasonUnparsed   = "unparsed output"
	reasonShutdown   = "daemon shutting down"
	detailSignedIn   = "signed in"
	detailFailedExit = "failed"
)

var (
	profileRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	codeRe    = regexp.MustCompile(`(?m)^[ \t]*([A-Z0-9]{4}-[A-Z0-9]{4})[ \t\r]*$`)
	urlRe     = regexp.MustCompile(`https://[^\s]+`)
	regionRe  = regexp.MustCompile(`^device\.sso\.[a-z]{2}(-[a-z]+)+-\d{1,2}\.amazonaws\.com$`)
	portalRe  = regexp.MustCompile(`^[a-z0-9-]+\.awsapps\.com$`)
)

var envKeep = map[string]bool{
	"HOME": true, "PATH": true, "LANG": true, "LC_ALL": true, "TZ": true,
	"AWS_CONFIG_FILE": true, "AWS_SHARED_CREDENTIALS_FILE": true, "AWS_CA_BUNDLE": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"HTTPS_PROXY": true, "https_proxy": true, "HTTP_PROXY": true, "http_proxy": true,
	"NO_PROXY": true, "no_proxy": true,
}

// Args is the fixed argv of the sign-in; the profile is checked by Start.
func Args(profile string) []string {
	return []string{"sso", "login", "--profile", profile, "--use-device-code", "--no-browser"}
}

func validProfile(p string) bool { return profileRe.MatchString(p) && !strings.HasPrefix(p, "-") }

// curatedEnv keeps, in input order, only the variables the CLI needs. The
// daemon's own secrets (tokens, AB_*, AWS_PROFILE, static keys) never reach it.
func curatedEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if envKeep[k] {
			out = append(out, kv)
		}
	}
	return out
}

// Parse extracts the sign-in URL and the user code from the CLI output. It
// needs both: a line that is only the code, and an https URL. A URL carrying
// user_code= (which autofills the code) is preferred.
func Parse(out string) (uri, code string, ok bool) {
	m := codeRe.FindStringSubmatch(out)
	if m == nil {
		return "", "", false
	}
	urls := urlRe.FindAllString(out, -1)
	if len(urls) == 0 {
		return "", "", false
	}
	uri = urls[0]
	for _, u := range urls {
		if strings.Contains(u, "user_code=") {
			uri = u
			break
		}
	}
	return uri, m[1], true
}

// TrustedURI reports whether raw is an https URL on AWS's device-authorization
// host or an Identity Center portal, with no userinfo and no port.
func TrustedURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return regionRe.MatchString(host) || portalRe.MatchString(host)
}

type flow struct {
	p          proc
	state      State
	reason     string // first failure reason recorded; the waiter reports it
	finishedAt time.Time
	ready      chan struct{} // closed once the code is parsed
	readyOnce  sync.Once
	readerDone chan struct{}
	once       sync.Once // guards finishing
	done       Done
}

type Runner struct {
	bin          string
	env          []string
	start        func(bin string, args, env []string) (proc, error)
	now          func() time.Time
	parseTimeout time.Duration
	flowTimeout  time.Duration
	keepDone     time.Duration

	mu     sync.Mutex
	flows  map[string]*flow
	closed bool
	wg     sync.WaitGroup
}

func New(awsBin string, environ []string) *Runner {
	return &Runner{
		bin:          awsBin,
		env:          curatedEnv(environ),
		start:        realStart,
		now:          time.Now,
		parseTimeout: 15 * time.Second,
		flowTimeout:  10 * time.Minute,
		keepDone:     2 * time.Minute,
		flows:        map[string]*flow{},
	}
}

// Start begins the sign-in for the SSO session key, or returns the one already
// running for it (a second click while waiting). It returns once the code is
// known; ErrUnparsed when the CLI printed none.
func (r *Runner) Start(key, profile string, done Done) (State, error) {
	if !validProfile(profile) {
		return State{}, errBadProfile
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return State{}, ErrClosed
	}
	r.sweepLocked()
	if f := r.flows[key]; f != nil && f.state.State == StateWaiting {
		r.mu.Unlock()
		return r.await(f)
	}
	p, err := r.start(r.bin, Args(profile), r.env)
	if err != nil {
		r.mu.Unlock()
		return State{}, errors.New("could not start aws sso login")
	}
	f := &flow{
		p:          p,
		state:      State{State: StateWaiting, Expires: r.now().Add(r.flowTimeout)},
		ready:      make(chan struct{}),
		readerDone: make(chan struct{}),
		done:       done,
	}
	r.flows[key] = f
	r.wg.Add(2)
	r.mu.Unlock()

	go r.read(f, p.Output())
	go r.wait(f)
	return r.await(f)
}

func (r *Runner) Status(key string) (State, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	f := r.flows[key]
	if f == nil {
		return State{}, false
	}
	return f.state, true
}

// Close kills every running flow and waits for the runner's goroutines.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	var procs []proc
	for _, f := range r.flows {
		if f.state.State == StateWaiting {
			if f.reason == "" {
				f.reason = reasonShutdown
			}
			procs = append(procs, f.p)
		}
	}
	r.mu.Unlock()
	for _, p := range procs {
		p.Kill()
	}
	r.wg.Wait()
}

func (r *Runner) sweepLocked() {
	for k, f := range r.flows {
		if f.state.State != StateWaiting && r.now().Sub(f.finishedAt) > r.keepDone {
			delete(r.flows, k)
		}
	}
}

// setReason records why the flow is about to fail. It is first-wins and must
// run before the kill it explains, so the waiter reports this reason.
func (r *Runner) setReason(f *flow, reason string) {
	r.mu.Lock()
	if f.reason == "" {
		f.reason = reason
	}
	r.mu.Unlock()
}

// await returns the flow's state once its code is parsed. A parse timeout
// kills the process; an output that ends without a code is left to the waiter,
// which reports the exit status.
func (r *Runner) await(f *flow) (State, error) {
	timer := time.NewTimer(r.parseTimeout)
	defer timer.Stop()
	timedOut := false
	select {
	case <-f.ready:
	case <-f.readerDone:
	case <-timer.C:
		timedOut = true
	}
	r.mu.Lock()
	st := f.state
	r.mu.Unlock()
	if st.Code != "" {
		return st, nil
	}
	if timedOut {
		r.setReason(f, reasonUnparsed)
		f.p.Kill()
	}
	return State{}, ErrUnparsed
}

// read scans the output for the URL and code and keeps draining to EOF so the
// CLI never blocks on a full pipe. Nothing but the parsed values is retained.
func (r *Runner) read(f *flow, out io.Reader) {
	defer r.wg.Done()
	defer close(f.readerDone)
	if c, ok := out.(io.Closer); ok {
		defer c.Close()
	}
	var buf []byte
	chunk := make([]byte, 4096)
	scanning := true
	for {
		n, err := out.Read(chunk)
		if scanning && (n > 0 || err != nil) {
			buf = append(buf, chunk[:min(n, maxOutput-len(buf))]...)
			text := string(buf)
			if err == nil { // only whole lines, so a URL is never cut short
				text = text[:strings.LastIndexByte(text, '\n')+1]
			}
			if uri, code, ok := Parse(text); ok {
				r.mu.Lock()
				f.state.URI, f.state.URITrusted, f.state.Code = uri, TrustedURI(uri), code
				r.mu.Unlock()
				f.readyOnce.Do(func() { close(f.ready) })
				scanning = !strings.Contains(uri, "user_code=")
			}
			if len(buf) >= maxOutput {
				scanning = false
			}
		}
		if err != nil {
			return
		}
	}
}

// wait reaps the process under the flow cap and finishes the flow.
func (r *Runner) wait(f *flow) {
	defer r.wg.Done()
	timer := time.AfterFunc(r.flowTimeout, func() {
		r.setReason(f, "timed out after "+r.flowTimeout.String())
		f.p.Kill()
	})
	err := f.p.Wait()
	timer.Stop()
	<-f.readerDone // the code is final before the verdict is

	r.mu.Lock()
	reason, noCode := f.reason, f.state.Code == ""
	r.mu.Unlock()
	switch {
	case reason != "":
		r.finish(f, false, reason)
	case err == nil && noCode:
		r.finish(f, false, reasonUnparsed)
	case err == nil:
		r.finish(f, true, detailSignedIn)
	default:
		detail := detailFailedExit
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() >= 0 {
			detail = "exit status " + strconv.Itoa(ee.ExitCode())
		}
		r.finish(f, false, detail)
	}
}

// finish runs done exactly once, then publishes the final state, so anything
// that observes the state also observes done's effects.
func (r *Runner) finish(f *flow, ok bool, detail string) {
	f.once.Do(func() {
		if f.done != nil {
			f.done(ok, detail)
		}
		r.mu.Lock()
		f.state.State = StateFailed
		if ok {
			f.state.State = StateOK
		}
		f.finishedAt = r.now()
		r.mu.Unlock()
	})
}
