package broker

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/config"
)

type simCall struct {
	Creds   aws.Credentials
	Region  string
	Role    string
	Actions []string
}

// fakeSim is a goroutine-safe awsprobe.Simulator. dec maps a role ARN to the
// reply (nil dec or nil reply = every action implicitly denied). errFor, when
// set and non-nil for a role, fails that role's simulation; err is the global
// fallback. delay waits
// (ctx-aware) before replying; a non-nil release channel blocks until it is
// closed or ctx ends. inflight/maxInflight track concurrency.
type fakeSim struct {
	mu          sync.Mutex
	dec         func(role string) map[string]string
	err         error
	errFor      func(role string) error
	delay       time.Duration
	release     chan struct{}
	calls       []simCall
	inflight    int
	maxInflight int
}

func (f *fakeSim) Simulate(ctx context.Context, creds aws.Credentials, region, role string, actions []string) (map[string]string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, simCall{Creds: creds, Region: region, Role: role, Actions: slices.Clone(actions)})
	f.inflight++
	f.maxInflight = max(f.maxInflight, f.inflight)
	dec, err, errFor, delay, release := f.dec, f.err, f.errFor, f.delay, f.release
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inflight--; f.mu.Unlock() }()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if errFor != nil {
		if e := errFor(role); e != nil {
			return nil, e
		}
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var reply map[string]string
	if dec != nil {
		reply = dec(role)
	}
	for _, a := range actions {
		out[a] = "implicitDeny"
	}
	for k, v := range reply {
		out[k] = v
	}
	return out, nil
}

func (f *fakeSim) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// maxConcurrent is the highest number of simultaneous Simulate calls seen.
func (f *fakeSim) maxConcurrent() int { f.mu.Lock(); defer f.mu.Unlock(); return f.maxInflight }

// callsCopy is a snapshot of the recorded calls, safe to read while Simulate runs.
func (f *fakeSim) callsCopy() []simCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func probeTarget() ProbeTarget {
	return ProbeTarget{SourceProfile: "sandbox", Region: "ap-southeast-1",
		RoleARN: "arn:aws:iam::123456789012:role/agent", S3Read: []string{"logs-bucket/app/"}}
}

func TestProbeRoleWrites(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	fs := &fakeSim{dec: func(string) map[string]string { return map[string]string{"s3:PutObject": "allowed"} }}
	tg := probeTarget()
	res := ProbeRole(context.Background(), fm, fs, tg, "agentbox-s-1", func() time.Time { return now })
	if res.State != awsprobe.Writes || !slices.Equal(res.Actions, []string{"s3:PutObject"}) || !res.CheckedAt.Equal(now) {
		t.Fatalf("res = %+v", res)
	}
	pol, err := awsscope.SessionPolicy("aws", tg.S3Read)
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.calls) != 1 {
		t.Fatalf("mints = %d", len(fm.calls))
	}
	in := fm.calls[0]
	if in.SourceProfile != tg.SourceProfile || in.Region != tg.Region || in.RoleARN != tg.RoleARN ||
		in.SessionName != "agentbox-s-1" || in.Duration != credTTL || in.Policy != pol {
		t.Fatalf("mint input = %+v", in)
	}
	if len(fs.calls) != 1 {
		t.Fatalf("simulations = %d", len(fs.calls))
	}
	c := fs.calls[0]
	if c.Creds.AccessKeyID != "ASIAFAKE" || c.Creds.SecretAccessKey != "s" || c.Creds.SessionToken != "t" ||
		c.Region != tg.Region || c.Role != tg.RoleARN || !slices.Equal(c.Actions, awsprobe.WriteActions) {
		t.Fatalf("sim call = %+v", c)
	}
}

func TestProbeRoleNoneFound(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	res := ProbeRole(context.Background(), fm, &fakeSim{}, probeTarget(), "agentbox-s-1", func() time.Time { return now })
	if res.State != awsprobe.NoneFound || !res.CheckedAt.Equal(now) {
		t.Fatalf("res = %+v", res)
	}
}

func TestProbeRoleMintReasons(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		err  error
		want awsprobe.Reason
	}{
		{"sso", &MintError{Code: "SSOExpired", cause: ErrSSOExpired}, awsprobe.ReasonSSOExpired},
		{"denied", &MintError{Code: "AccessDenied"}, awsprobe.ReasonAssumeDenied},
		{"timeout", &MintError{Code: "Timeout"}, awsprobe.ReasonTimeout},
		{"config", &MintError{Code: "ConfigError"}, awsprobe.ReasonError},
		{"plain", errors.New("boom"), awsprobe.ReasonError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fm := &fakeMinter{now: func() time.Time { return now }, err: tc.err}
			fs := &fakeSim{}
			res := ProbeRole(context.Background(), fm, fs, probeTarget(), "agentbox-s-1", func() time.Time { return now })
			if res.State != awsprobe.Unverified || res.Reason != tc.want || !res.CheckedAt.Equal(now) {
				t.Fatalf("res = %+v, want reason %s", res, tc.want)
			}
			if fs.callCount() != 0 {
				t.Fatal("simulator was called after a failed mint")
			}
		})
	}
}

func TestProbeRoleMintTimeoutByContext(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fm := &fakeMinter{now: func() time.Time { return now }, err: errors.New("context canceled")}
	res := ProbeRole(ctx, fm, &fakeSim{}, probeTarget(), "agentbox-s-1", func() time.Time { return now })
	if res.Reason != awsprobe.ReasonTimeout {
		t.Fatalf("res = %+v", res)
	}
}

func TestProbeRoleSimulateDenied(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	fs := &fakeSim{err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "arn:aws:iam::123456789012:role/agent"}}
	res := ProbeRole(context.Background(), fm, fs, probeTarget(), "agentbox-s-1", func() time.Time { return now })
	if res.State != awsprobe.Unverified || res.Reason != awsprobe.ReasonSimulateDenied {
		t.Fatalf("res = %+v", res)
	}
}

func TestProbeRoleSimulateOtherError(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	res := ProbeRole(context.Background(), fm, &fakeSim{err: errors.New("boom")}, probeTarget(), "n", func() time.Time { return now })
	if res.State != awsprobe.Unverified || res.Reason != awsprobe.ReasonError {
		t.Fatalf("res = %+v", res)
	}
}

func TestProbeRoleTimeout(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	fs := &fakeSim{release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := ProbeRole(ctx, fm, fs, probeTarget(), "agentbox-s-1", func() time.Time { return now })
	if time.Since(start) > time.Second {
		t.Fatal("did not return promptly after the deadline")
	}
	if res.State != awsprobe.Unverified || res.Reason != awsprobe.ReasonTimeout {
		t.Fatalf("res = %+v", res)
	}
}

func TestProbeRoleGovCloudPartition(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	tg := ProbeTarget{SourceProfile: "gov", Region: "us-gov-west-1", RoleARN: "arn:aws-us-gov:iam::123456789012:role/r",
		S3Read: []string{"logs-bucket/app/"}}
	ProbeRole(context.Background(), fm, &fakeSim{}, tg, "agentbox-s-1", func() time.Time { return now })
	if len(fm.calls) != 1 {
		t.Fatalf("mints = %d", len(fm.calls))
	}
	pol := fm.calls[0].Policy
	if !strings.Contains(pol, "arn:aws-us-gov:s3:::logs-bucket/app/") || strings.Contains(pol, "arn:aws:s3:::") {
		t.Fatalf("policy is not built for the aws-us-gov partition: %s", pol)
	}
}

func TestProbeRoleInvalidRoleARN(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fm := &fakeMinter{now: func() time.Time { return now }}
	fs := &fakeSim{}
	tg := ProbeTarget{SourceProfile: "gov", Region: "us-gov-west-1", RoleARN: "arn:aws-us-gov:iam::1:role/r"}
	res := ProbeRole(context.Background(), fm, fs, tg, "agentbox-s-1", func() time.Time { return now })
	if res.State != awsprobe.Unverified || res.Reason != awsprobe.ReasonError || !res.CheckedAt.Equal(now) {
		t.Fatalf("res = %+v", res)
	}
	if len(fm.calls) != 0 || fs.callCount() != 0 {
		t.Fatalf("mints = %d, simulations = %d; want none", len(fm.calls), fs.callCount())
	}
}

func TestTargetOf(t *testing.T) {
	b := config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::1:role/r", Region: "eu-west-1", S3Read: []string{"b/"}}
	got := TargetOf(b)
	want := ProbeTarget{SourceProfile: "p", Region: "eu-west-1", RoleARN: "arn:aws:iam::1:role/r", S3Read: []string{"b/"}}
	if got.SourceProfile != want.SourceProfile || got.Region != want.Region || got.RoleARN != want.RoleARN || !slices.Equal(got.S3Read, want.S3Read) {
		t.Fatalf("got %+v", got)
	}
}

func TestFakeSimErrForIsPerRole(t *testing.T) {
	fs := &fakeSim{errFor: func(role string) error {
		if role == "bad" {
			return errors.New("boom")
		}
		return nil
	}}
	if _, err := fs.Simulate(context.Background(), aws.Credentials{}, "r", "bad", awsprobe.WriteActions); err == nil {
		t.Fatal("bad role should fail")
	}
	if _, err := fs.Simulate(context.Background(), aws.Credentials{}, "r", "good", awsprobe.WriteActions); err != nil {
		t.Fatal(err)
	}
	if fs.callCount() != 2 || len(fs.callsCopy()) != 2 || fs.maxConcurrent() != 1 {
		t.Fatalf("calls=%d max=%d", fs.callCount(), fs.maxConcurrent())
	}
}

// --- ProbeBase ---

const (
	roleA = "arn:aws:iam::111111111111:role/a"
	roleB = "arn:aws:iam::222222222222:role/b"
	roleC = "arn:aws:iam::333333333333:role/c"
)

func connOf(name, src, role, region string, s3 ...string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{SourceProfile: src, RoleARN: role, Region: region, S3Read: s3}}
}

func probeSetup(t *testing.T, sim *fakeSim) (*Broker, *fakeMinter, *memRec, *time.Time) {
	t.Helper()
	b, fm, rec, now := setup(t, nil)
	if sim != nil {
		b.SetSimulator(sim)
	}
	return b, fm, rec, now
}

func TestProbeBaseDisabledWithoutSimulator(t *testing.T) {
	b, fm, rec, _ := probeSetup(t, nil)
	got := b.ProbeBase("s-1", "claude", config.Bindings{AWS: binding()})
	if len(got) != 1 || !reflect.DeepEqual(got[""], awsprobe.Disabled()) {
		t.Fatalf("got %+v", got)
	}
	if len(fm.calls) != 0 || len(rec.rows) != 0 {
		t.Fatalf("mints=%d rows=%d", len(fm.calls), len(rec.rows))
	}
	conns := b.ProbeBase("s-1", "claude", config.Bindings{AWSConns: []config.AWSConnection{connOf("a", "p", roleA, "r")}})
	if len(conns) != 1 || conns["a"].Reason != awsprobe.ReasonProbeDisabled {
		t.Fatalf("conns = %+v", conns)
	}
}

func TestProbeBaseNoAWSNoProbe(t *testing.T) {
	b, fm, rec, _ := probeSetup(t, &fakeSim{})
	got := b.ProbeBase("s-1", "claude", config.Bindings{GitHub: &config.GitHubBinding{}})
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
	if len(fm.calls) != 0 || len(rec.rows) != 0 {
		t.Fatalf("mints=%d rows=%d", len(fm.calls), len(rec.rows))
	}
}

func TestProbeBaseConnections(t *testing.T) {
	fs := &fakeSim{dec: func(role string) map[string]string {
		if role == roleB {
			return map[string]string{"s3:PutObject": "allowed"}
		}
		return nil
	}}
	b, fm, _, now := probeSetup(t, fs)
	got := b.ProbeBase("s-1", "claude", config.Bindings{AWSConns: []config.AWSConnection{
		connOf("a", "pa", roleA, "r1"), connOf("b", "pb", roleB, "r1")}})
	if got["a"].State != awsprobe.NoneFound || got["b"].State != awsprobe.Writes ||
		!slices.Equal(got["b"].Actions, []string{"s3:PutObject"}) || len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if !got["a"].CheckedAt.Equal(*now) {
		t.Fatalf("CheckedAt = %v", got["a"].CheckedAt)
	}
	if len(fm.calls) != 2 || fs.callCount() != 2 {
		t.Fatalf("mints=%d sims=%d", len(fm.calls), fs.callCount())
	}
	for _, c := range fm.calls {
		if c.SessionName != awsscope.SessionName("s-1") {
			t.Fatalf("session name %q", c.SessionName)
		}
	}
}

func TestProbeBaseDedupesSamePair(t *testing.T) {
	fs := &fakeSim{dec: func(string) map[string]string { return map[string]string{"s3:PutObject": "allowed"} }}
	b, fm, rec, _ := probeSetup(t, fs)
	got := b.ProbeBase("s-1", "claude", config.Bindings{AWSConns: []config.AWSConnection{
		connOf("b", "p", roleA, "eu-west-1", "bucket/x/"), connOf("a", "p", roleA, "ap-southeast-1")}})
	if len(fm.calls) != 1 || fs.callCount() != 1 {
		t.Fatalf("mints=%d sims=%d", len(fm.calls), fs.callCount())
	}
	if len(got) != 2 || !reflect.DeepEqual(got["a"], got["b"]) || got["a"].State != awsprobe.Writes {
		t.Fatalf("got %+v", got)
	}
	if len(rec.rows) != 1 || rec.rows[0].Entry != "a" {
		t.Fatalf("rows = %+v", rec.rows)
	}
}

func TestProbeBaseParallel(t *testing.T) {
	fs := &fakeSim{release: make(chan struct{})}
	b, _, _, _ := probeSetup(t, fs)
	done := make(chan map[string]awsprobe.Result)
	go func() {
		done <- b.ProbeBase("s-1", "claude", config.Bindings{AWSConns: []config.AWSConnection{
			connOf("a", "p", roleA, "r"), connOf("b", "p", roleB, "r"), connOf("c", "p", roleC, "r")}})
	}()
	// All three simulations must be in flight together before any is released.
	deadline := time.Now().Add(5 * time.Second)
	for fs.maxConcurrent() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("maxConcurrent = %d, want 3", fs.maxConcurrent())
		}
		time.Sleep(time.Millisecond)
	}
	close(fs.release)
	if got := <-done; len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
}

func TestProbeBaseTimeoutIsUnverified(t *testing.T) {
	fs := &fakeSim{release: make(chan struct{})} // never released
	b, _, rec, _ := probeSetup(t, fs)
	b.probeTimeout = 30 * time.Millisecond
	start := time.Now()
	got := b.ProbeBase("s-1", "claude", config.Bindings{AWS: binding()})
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if got[""].State != awsprobe.Unverified || got[""].Reason != awsprobe.ReasonTimeout {
		t.Fatalf("got %+v", got)
	}
	if len(b.probeCache) != 0 {
		t.Fatal("unverified result was cached")
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeProbeMinted {
		t.Fatalf("mint succeeded, so one probe_minted row: %+v", rec.rows)
	}
}

func TestProbeBaseCache(t *testing.T) {
	fs := &fakeSim{dec: func(string) map[string]string { return map[string]string{"s3:PutObject": "allowed"} }}
	b, fm, rec, now := probeSetup(t, fs)
	bs := config.Bindings{AWS: binding()}
	first := b.ProbeBase("s-1", "claude", bs)
	*now = now.Add(59 * time.Minute)
	second := b.ProbeBase("s-2", "claude", bs)
	if len(fm.calls) != 1 || fs.callCount() != 1 || len(rec.rows) != 1 {
		t.Fatalf("mints=%d sims=%d rows=%d", len(fm.calls), fs.callCount(), len(rec.rows))
	}
	if !second[""].CheckedAt.Equal(first[""].CheckedAt) || second[""].State != awsprobe.Writes {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	*now = now.Add(2 * time.Minute) // 61 minutes after the first probe
	third := b.ProbeBase("s-3", "claude", bs)
	if len(fm.calls) != 2 || !third[""].CheckedAt.Equal(*now) {
		t.Fatalf("mints=%d third=%+v", len(fm.calls), third)
	}

	// An unverified result is never cached.
	fs2 := &fakeSim{err: errors.New("boom")}
	b2, fm2, _, _ := probeSetup(t, fs2)
	for i := 0; i < 2; i++ {
		if r := b2.ProbeBase("s-1", "claude", bs); r[""].State != awsprobe.Unverified {
			t.Fatalf("r = %+v", r)
		}
	}
	if len(fm2.calls) != 2 || len(b2.probeCache) != 0 {
		t.Fatalf("mints=%d cache=%d", len(fm2.calls), len(b2.probeCache))
	}
}

func TestProbeBaseAuditRows(t *testing.T) {
	fs := &fakeSim{}
	b, fm, rec, now := probeSetup(t, fs)
	b.ProbeBase("s-1", "claude", config.Bindings{AWSConns: []config.AWSConnection{
		connOf("z", "p", roleA, "r"), connOf("m", "p", roleA, "r")}})
	if len(rec.rows) != 1 {
		t.Fatalf("rows = %+v", rec.rows)
	}
	r := rec.rows[0]
	if r.Outcome != OutcomeProbeMinted || r.SessionID != "s-1" || r.Profile != "claude" || r.RoleARN != roleA ||
		r.AccountID != "111111111111" || r.SourceIdentity != awsscope.SessionName("s-1") || r.Entry != "m" ||
		!r.Expires.Equal(now.Add(credTTL)) || r.Elevated || r.Error != "" || !r.Time.Equal(*now) {
		t.Fatalf("row = %+v", r)
	}

	// A cache hit records nothing.
	b.ProbeBase("s-2", "claude", config.Bindings{AWSConns: []config.AWSConnection{connOf("m", "p", roleA, "r")}})
	if len(rec.rows) != 1 {
		t.Fatalf("cache hit wrote a row: %+v", rec.rows)
	}

	// A mint failure records probe_failed with a sanitized error.
	fm.err = &MintError{Code: "AccessDenied", Detail: "not authorized\n  to assume " + roleB}
	res := b.ProbeBase("s-3", "claude", config.Bindings{AWSConns: []config.AWSConnection{connOf("q", "p", roleB, "r")}})
	if res["q"].Reason != awsprobe.ReasonAssumeDenied {
		t.Fatalf("res = %+v", res)
	}
	if len(rec.rows) != 2 {
		t.Fatalf("rows = %+v", rec.rows)
	}
	f := rec.rows[1]
	if f.Outcome != OutcomeProbeFailed || f.Error == "" || strings.ContainsAny(f.Error, "\n\t") || f.Entry != "q" ||
		!f.Expires.IsZero() || f.SourceIdentity != awsscope.SessionName("s-3") {
		t.Fatalf("failed row = %+v", f)
	}

	// A failing Recorder does not change the result.
	fm.mu.Lock()
	fm.err = nil
	fm.mu.Unlock()
	rec.mu.Lock()
	rec.recErr = errors.New("disk full")
	rec.mu.Unlock()
	got := b.ProbeBase("s-4", "claude", config.Bindings{AWSConns: []config.AWSConnection{connOf("n", "p", roleC, "r")}})
	if got["n"].State != awsprobe.NoneFound {
		t.Fatalf("got %+v", got)
	}
}

func TestProbeBaseLeavesMintStateAlone(t *testing.T) {
	fs := &fakeSim{}
	b, fm, _, _ := probeSetup(t, fs)
	fm.err = &MintError{Code: "SSOExpired", Detail: "x", cause: ErrSSOExpired}
	res := b.ProbeBase("s-1", "claude", config.Bindings{AWS: binding()})
	if res[""].Reason != awsprobe.ReasonSSOExpired {
		t.Fatalf("res = %+v", res)
	}
	fm.err = nil
	b.ProbeBase("s-1", "claude", config.Bindings{AWSConns: []config.AWSConnection{connOf("a", "p", roleA, "r")}})
	if b.SSOExpired("s-1") != "" {
		t.Fatalf("SSOExpired = %q", b.SSOExpired("s-1"))
	}
	if _, ok := b.Stats("s-1"); ok {
		t.Fatal("probe created Stats")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.cache) != 0 || len(b.mintTimes) != 0 || len(b.ssoBad) != 0 || len(b.ssoNeed) != 0 ||
		len(b.stats) != 0 || len(b.connStats) != 0 {
		t.Fatalf("mint state touched: cache=%d times=%d ssoBad=%d ssoNeed=%d stats=%d conn=%d",
			len(b.cache), len(b.mintTimes), len(b.ssoBad), len(b.ssoNeed), len(b.stats), len(b.connStats))
	}
}
