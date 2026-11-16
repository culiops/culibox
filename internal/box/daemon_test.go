package box

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// #154: a docker that is installed but whose daemon cannot be used is told apart
// by what the CLI prints. The texts are what docker prints (the Inspect test
// pins the same "Cannot connect" line).
func TestPingClassifiesTheDaemon(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		code   int
		want   error // nil: the daemon answers, or (other) the error is neither class
		other  bool
	}{
		{name: "healthy", code: 0},
		{name: "daemon stopped (socket present, nobody listens)", code: 1,
			stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", want: ErrDockerNotRunning},
		{name: "daemon stopped (socket gone)", code: 1,
			stderr: "failed to connect to the docker API at unix:///var/run/docker.sock; check if the path is correct and if the daemon is running: dial unix /var/run/docker.sock: connect: no such file or directory", want: ErrDockerNotRunning},
		{name: "refused", code: 1, stderr: "error during connect: Get http://x/v1.40/info: dial tcp 127.0.0.1:2375: connect: connection refused", want: ErrDockerNotRunning},
		{name: "not in the docker group", code: 1,
			stderr: "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.47/info\": dial unix /var/run/docker.sock: connect: permission denied", want: ErrDockerDenied},
		{name: "older docker wording", code: 1,
			stderr: "Got permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock", want: ErrDockerDenied},
		{name: "something else", code: 1, stderr: "error getting credentials - err: exit status 1", other: true},
	} {
		bin, _ := fakeDockerStderr(t, map[string]string{"info": ""}, map[string]string{"info": tc.stderr}, map[string]int{"info": tc.code})
		err := CLIClient{Bin: bin}.Ping(context.Background())
		switch {
		case tc.want == nil && !tc.other:
			if err != nil {
				t.Errorf("%s: want no error, got %v", tc.name, err)
			}
		case tc.other:
			if err == nil || errors.Is(err, ErrDockerNotRunning) || errors.Is(err, ErrDockerDenied) {
				t.Errorf("%s: want an unclassified error, got %v", tc.name, err)
			}
		default:
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
			}
			if errors.Is(err, ErrDockerNotRunning) == errors.Is(err, ErrDockerDenied) {
				t.Errorf("%s: must be exactly one class: %v", tc.name, err)
			}
		}
	}
}

// The probe carries the endpoint flags like every other call, and is bounded:
// a daemon that never answers must not hold boot or /api/meta.
func TestPingCarriesFlagsAndIsBounded(t *testing.T) {
	bin, log := fakeDocker(t, map[string]string{"info": "24.0"}, nil)
	c := CLIClient{Bin: bin, Args: []string{"--host", "unix:///v/docker.sock"}}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.HasPrefix(got, "--host unix:///v/docker.sock info") {
		t.Fatalf("the probe did not carry the endpoint flags: %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("a cancelled probe must fail")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a cancelled probe did not return at once")
	}
	if ProbeTimeout <= 0 || ProbeTimeout > 10*time.Second {
		t.Fatalf("ProbeTimeout %v is not a short bound", ProbeTimeout)
	}
}

// The sentence is plain: never the CLI's own text, which carries socket paths.
func TestDaemonReasonSentences(t *testing.T) {
	raw := "dial unix /var/run/docker.sock: connect: permission denied"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{errors.Join(ErrDockerNotRunning, errors.New(raw)), "Docker is not running"},
		{errors.Join(ErrDockerDenied, errors.New(raw)), "no permission to use Docker"},
		{errors.New(raw), "run `culibox doctor` for details"},
	} {
		got := DaemonReason(tc.err)
		if got != tc.want {
			t.Errorf("DaemonReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
		if strings.Contains(got, "/") {
			t.Errorf("%q carries a path", got)
		}
	}
}

// deadlineClient records whether the boot-time runtime check was given a bound.
type deadlineClient struct {
	*fakeClient
	bounded bool
	left    time.Duration
}

func (d *deadlineClient) HasRuntime(ctx context.Context, name string) bool {
	if dl, ok := ctx.Deadline(); ok {
		d.bounded, d.left = true, time.Until(dl)
	}
	return d.fakeClient.HasRuntime(ctx, name)
}

// #154: boot asked the daemon about its runtimes with no deadline, so a hung
// daemon held the whole start. New bounds it.
func TestNewBoundsTheRuntimeProbe(t *testing.T) {
	c := &deadlineClient{fakeClient: newFake()}
	if _, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: c}); err != nil {
		t.Fatal(err)
	}
	if !c.bounded || c.left > ProbeTimeout {
		t.Fatalf("the runtime probe at boot must carry a deadline of at most %v (bounded=%v, left=%v)", ProbeTimeout, c.bounded, c.left)
	}
}
