package broker

import (
	"context"
	"errors"
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
