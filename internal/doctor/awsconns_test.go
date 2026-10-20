package doctor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

// fakeAWS answers the aws CLI calls checkAWSConns makes. profiles is what
// `aws configure list-profiles` prints; sso maps a profile to its sso_session;
// expired lists profiles whose sts call fails.
type fakeAWS struct {
	mu       sync.Mutex
	profiles []string
	sso      map[string]string
	expired  map[string]bool
	calls    []string
}

func (f *fakeAWS) Run(_ context.Context, _ []string, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch {
	case len(args) >= 2 && args[0] == "configure" && args[1] == "list-profiles":
		return strings.Join(f.profiles, "\n") + "\n", nil
	case len(args) >= 5 && args[0] == "configure" && args[1] == "get":
		if args[2] == "sso_session" {
			if s, ok := f.sso[args[4]]; ok {
				return s + "\n", nil
			}
		}
		return "", errors.New("exit status 1")
	case len(args) >= 3 && args[0] == "sts":
		if f.expired[args[3]] {
			return "", errors.New("The SSO session associated with this profile has expired")
		}
		return "111111111111\n", nil
	}
	return "", errors.New("unexpected: " + strings.Join(args, " "))
}

func (f *fakeAWS) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func conn(name, src string, elevate bool) config.AWSConnection {
	c := config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{
		SourceProfile: src, RoleARN: "arn:aws:iam::111111111111:role/agent", Region: "ap-southeast-1"}}
	if elevate {
		c.Elevate = &config.AWSElevate{RoleARN: "arn:aws:iam::111111111111:role/agent-write"}
	}
	return c
}

func TestDoctorAWSConnLines(t *testing.T) {
	f := &fakeAWS{
		profiles: []string{"corp-sso", "legacy"},
		sso:      map[string]string{"corp-sso": "corp"},
	}
	c := config.Config{AWSConnections: []config.AWSConnection{
		conn("staging", "corp-sso", false),
		conn("prod", "corp-sso", true),
		conn("gone", "no-such-profile", false),
		conn("old", "legacy", false),
	}}
	got := map[string]Result{}
	for _, r := range checkAWSConns(context.Background(), c, f) {
		got[r.Check] = r
	}
	if len(got) != 4 {
		t.Fatalf("want one line per connection, got %v", got)
	}
	want := map[string][2]string{
		"aws-connection staging": {Pass, "source_profile corp-sso ok; sso valid; elevate no"},
		"aws-connection prod":    {Pass, "source_profile corp-sso ok; sso valid; elevate yes"},
		"aws-connection gone":    {Fail, "source_profile no-such-profile missing; sso n/a; elevate no"},
		"aws-connection old":     {Pass, "source_profile legacy ok; sso n/a; elevate no"},
	}
	for k, w := range want {
		if got[k].Status != w[0] || got[k].Detail != w[1] {
			t.Errorf("%s = %s %q, want %s %q", k, got[k].Status, got[k].Detail, w[0], w[1])
		}
	}
	// staging and prod share the sso_session "corp": one sign-in check.
	if n := f.count("sts "); n != 1 {
		t.Errorf("sts calls = %d, want 1 (SSO check deduped by sourceKey); calls=%v", n, f.calls)
	}
}

func TestDoctorAWSConnExpiredSSO(t *testing.T) {
	f := &fakeAWS{
		profiles: []string{"a", "b"},
		sso:      map[string]string{"a": "corp", "b": "corp"},
		expired:  map[string]bool{"a": true},
	}
	c := config.Config{AWSConnections: []config.AWSConnection{conn("one", "a", false), conn("two", "b", false)}}
	for _, r := range checkAWSConns(context.Background(), c, f) {
		if r.Status != Warn || !strings.Contains(r.Detail, "sso expired") {
			t.Errorf("%s = %s %q, want WARN with sso expired for both on one session", r.Check, r.Status, r.Detail)
		}
	}
}

func TestDoctorAWSConnNone(t *testing.T) {
	f := &fakeAWS{}
	if got := checkAWSConns(context.Background(), config.Config{}, f); len(got) != 0 {
		t.Errorf("no connections should print nothing, got %v", got)
	}
	if len(f.calls) != 0 {
		t.Errorf("no connections should not run aws, calls=%v", f.calls)
	}
}

func TestDoctorAWSConnNoCLI(t *testing.T) {
	c := config.Config{AWSConnections: []config.AWSConnection{conn("x", "p", false)}}
	rs := checkAWSConns(context.Background(), c, failRunner{})
	if len(rs) != 1 || rs[0].Status != Warn || !strings.Contains(rs[0].Detail, "cannot check") {
		t.Errorf("got %v, want one WARN 'cannot check'", rs)
	}
}

type failRunner struct{}

func (failRunner) Run(context.Context, []string, string, ...string) (string, error) {
	return "", errors.New("aws: executable file not found in $PATH")
}
