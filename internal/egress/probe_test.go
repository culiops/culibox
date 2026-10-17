package egress

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const goodLines = "DIRECT BLOCKED ENETUNREACH\nDNS BLOCKED EAI_AGAIN\nALLOW 200\nDENY 403\n"

type probeIn struct {
	User, Pass, Host string
	Port             int
	Allow, Deny      string
	Direct           string
}

func parseIn(t *testing.T, stdin string) probeIn {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdin)), &raw); err != nil {
		t.Fatalf("stdin is not one JSON line: %v", err)
	}
	s := func(k string) string { v, _ := raw[k].(string); return v }
	port, _ := raw["port"].(float64)
	return probeIn{s("user"), s("pass"), s("host"), int(port), s("allow"), s("deny"), s("direct")}
}

func TestProbeScriptParsesInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	// node refuses --check together with -e, so check a file.
	f := filepath.Join(t.TempDir(), "probe.js")
	if err := os.WriteFile(f, []byte(probeScript()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("probe script does not parse: %v\n%s", err, out)
	}
}

func hostNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	return node
}

// nodeExec runs the script with the host's node, the way `docker exec -i` would.
func nodeExec(t *testing.T, node string) ExecInput {
	return func(ctx context.Context, script, stdin string) (string, error) {
		cmd := exec.CommandContext(ctx, node, "-e", script)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
}

// A real run against a real proxy on 127.0.0.1, with the script run by the
// host's node: allow reaches the harness upstream, deny hits the allowlist,
// direct is a closed port and the DNS name is invalid.
func TestProbePassesAgainstARealProxy(t *testing.T) {
	node := hostNode(t)
	h := newHarness(t, nil)
	host, port, _ := net.SplitHostPort(h.addr)
	pn, _ := strconv.Atoi(port)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: host, Port: pn, DenyHost: "probe.invalid", Direct: "127.0.0.1:1"}, nodeExec(t, node))
	if err != nil {
		t.Fatalf("probe against a good setup failed: %v", err)
	}
}

// The script must report "reached" when the direct target answers, and the
// verdict must be a containment failure.
func TestProbeRealNodeSeesDirectReach(t *testing.T) {
	node := hostNode(t)
	h := newHarness(t, nil)
	host, port, _ := net.SplitHostPort(h.addr)
	pn, _ := strconv.Atoi(port)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: host, Port: pn, DenyHost: "probe.invalid", Direct: h.echo.Addr().String()}, nodeExec(t, node))
	if err == nil || !strings.Contains(err.Error(), "directly") {
		t.Fatalf("a reachable direct target must fail as containment: %v", err)
	}
}

// Garbage on stdin must not be echoed back by the script (node's JSON.parse
// error quotes the input).
func TestProbeScriptDoesNotEchoBadStdin(t *testing.T) {
	node := hostNode(t)
	out, _ := nodeExec(t, node)(context.Background(), probeScript(), "{not json SECRET-IN-STDIN\n")
	if strings.Contains(out, "SECRET-IN-STDIN") {
		t.Fatalf("script echoed its stdin: %q", out)
	}
}

func TestProbeFailsClosed(t *testing.T) {
	cases := map[string]struct{ out, want string }{
		"direct reached": {"DIRECT REACHED\nDNS BLOCKED EAI_AGAIN\nALLOW 200\nDENY 403\n", "directly"},
		"dns resolved":   {"DIRECT BLOCKED ENETUNREACH\nDNS RESOLVED\nALLOW 200\nDENY 403\n", "resolve"},
		"allow refused":  {"DIRECT BLOCKED ENETUNREACH\nDNS BLOCKED EAI_AGAIN\nALLOW 407\nDENY 403\n", "credential"},
		"deny open":      {"DIRECT BLOCKED ENETUNREACH\nDNS BLOCKED EAI_AGAIN\nALLOW 200\nDENY 200\n", "not allowlisted"},
		"missing line":   {"DIRECT BLOCKED ENETUNREACH\nALLOW 200\nDENY 403\n", "unexpected"},
		"extra line":     {goodLines + "ALLOW 200\n", "unexpected"},
		"reordered":      {"DNS BLOCKED EAI_AGAIN\nDIRECT BLOCKED ENETUNREACH\nALLOW 200\nDENY 403\n", "unexpected"},
		"junk code":      {"DIRECT BLOCKED x y\nDNS BLOCKED EAI_AGAIN\nALLOW 200\nDENY 403\n", "unexpected"},
		"empty":          {"", "unexpected"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
				func(context.Context, string, string) (string, error) { return c.out, nil })
			if err == nil {
				t.Fatal("Probe passed")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// A 503 or a dial_error on the ALLOW leg spends the allowed slot and fails
// closed, but it is not a containment failure and must not say so.
func TestProbeBusyOrUnreachableUpstreamIsNamedHonestly(t *testing.T) {
	for _, st := range []string{"503", "502", "504"} {
		t.Run(st, func(t *testing.T) {
			h := newHarness(t, nil)
			err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
				func(context.Context, string, string) (string, error) {
					return "DIRECT BLOCKED ENETUNREACH\nDNS BLOCKED EAI_AGAIN\nALLOW " + st + "\nDENY 403\n", nil
				})
			if err == nil {
				t.Fatal("Probe passed")
			}
			m := err.Error()
			if !strings.Contains(m, "busy / upstream unreachable") {
				t.Fatalf("error %q must say the proxy is busy or the upstream unreachable", m)
			}
			for _, bad := range []string{"directly", "resolve", "not allowlisted", "contain"} {
				if strings.Contains(m, bad) {
					t.Fatalf("error %q claims a containment failure (%q)", m, bad)
				}
			}
		})
	}
}

// The real proxy answers 502 when the upstream cannot be dialled; the probe
// driven by that real answer must fail with the honest message.
func TestProbeRealDialErrorFailsAsUnreachable(t *testing.T) {
	h := newHarness(t, nil)
	h.mu.Lock()
	h.dialErr = func(netip.AddrPort) error { return errors.New("down") }
	h.mu.Unlock()
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(_ context.Context, _, stdin string) (string, error) {
			in := parseIn(t, stdin)
			code, _ := h.connect(t, in.Allow+":443", in.User, in.Pass)
			dcode, _ := h.connect(t, in.Deny+":443", in.User, in.Pass)
			return "DIRECT BLOCKED ENETUNREACH\nDNS BLOCKED EAI_AGAIN\nALLOW " + strconv.Itoa(code) + "\nDENY " + strconv.Itoa(dcode) + "\n", nil
		})
	if err == nil || !strings.Contains(err.Error(), "busy / upstream unreachable") || strings.Contains(err.Error(), "directly") {
		t.Fatalf("got %v", err)
	}
}

// A containment line outranks a busy proxy: if the box can get out directly,
// that is what the error says even though ALLOW also failed.
func TestProbeContainmentFailureOutranksBusy(t *testing.T) {
	h := newHarness(t, nil)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(context.Context, string, string) (string, error) {
			return "DIRECT REACHED\nDNS BLOCKED EAI_AGAIN\nALLOW 503\nDENY 403\n", nil
		})
	if err == nil || !strings.Contains(err.Error(), "directly") {
		t.Fatalf("got %v", err)
	}
}

// A script that prints the right four lines but never talked to the proxy must fail.
func TestProbeFailsWhenTheProxySawNothing(t *testing.T) {
	h := newHarness(t, nil)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(context.Context, string, string) (string, error) { return goodLines, nil })
	if err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("a lying script passed or the error is unclear: %v", err)
	}
}

// A script that makes only the allowed CONNECT and claims the deny too.
func TestProbeFailsWhenOnlyOneLegReachedTheProxy(t *testing.T) {
	h := newHarness(t, nil)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(_ context.Context, _, stdin string) (string, error) {
			in := parseIn(t, stdin)
			code, c := h.connect(t, in.Allow+":443", in.User, in.Pass)
			if code != 200 {
				t.Fatalf("allow leg: %d", code)
			}
			c.Close()
			return goodLines, nil
		})
	if err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("got %v", err)
	}
}

// Both legs made through the real proxy, lines honest: pass. This also covers
// the harness-driven path with no node.
func TestProbePassesWithBothLegsMadeThroughTheProxy(t *testing.T) {
	h := newHarness(t, nil)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(_ context.Context, _, stdin string) (string, error) {
			in := parseIn(t, stdin)
			if in.Host != "10.0.0.1" || in.Port != 9072 || in.Deny != "example.com" || in.Direct != "1.1.1.1:443" || in.Allow != "api.anthropic.com" {
				t.Errorf("defaults wrong: %+v", in)
			}
			code, c := h.connect(t, in.Allow+":443", in.User, in.Pass)
			if code != 200 {
				t.Fatalf("allow leg: %d", code)
			}
			c.Close()
			if code, _ := h.connect(t, in.Deny+":443", in.User, in.Pass); code != 403 {
				t.Fatalf("deny leg: %d", code)
			}
			return goodLines, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

// The credential is single-use: after Probe returns, the same user and password
// are 407.
func TestProbeCredentialIsRetiredAfterProbe(t *testing.T) {
	h := newHarness(t, nil)
	var in probeIn
	Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(_ context.Context, _, stdin string) (string, error) {
			in = parseIn(t, stdin)
			return "", errors.New("boom")
		})
	if code, _ := h.connect(t, "api.anthropic.com:443", in.User, in.Pass); code != 407 {
		t.Fatalf("credential outlived Probe: %d", code)
	}
}

func TestProbeExecErrorFails(t *testing.T) {
	h := newHarness(t, nil)
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(context.Context, string, string) (string, error) {
			return goodLines, errors.New("docker exec: no such container")
		})
	if err == nil {
		t.Fatal("an exec error must fail the probe even when the output looks right")
	}
}

func TestProbeCancelledContextFails(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Bool
	err := Probe(ctx, h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(ctx context.Context, _, _ string) (string, error) { ran.Store(true); return goodLines, ctx.Err() })
	if err == nil {
		t.Fatal("a cancelled probe passed")
	}
}

func TestProbeRejectsABadTarget(t *testing.T) {
	h := newHarness(t, nil)
	run := func(context.Context, string, string) (string, error) { t.Fatal("exec ran"); return "", nil }
	for _, tg := range []ProbeTarget{{ProxyHost: "", Port: 9072}, {ProxyHost: "10.0.0.1", Port: 0}, {ProxyHost: "10.0.0.1", Port: 70000}} {
		if err := Probe(context.Background(), h.p, "box-1", tg, run); err == nil {
			t.Fatalf("%+v accepted", tg)
		}
	}
}

// The script text is fixed: it holds no user, password or proxy URL, because
// those travel on stdin.
func TestProbeCredentialNeverInScript(t *testing.T) {
	h := newHarness(t, nil)
	var script, stdin string
	Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(_ context.Context, s, in string) (string, error) { script, stdin = s, in; return goodLines, nil })
	in := parseIn(t, stdin)
	if in.User == "" || in.Pass == "" {
		t.Fatalf("stdin lacks the credential: %q", stdin)
	}
	for _, secret := range []string{in.User, in.Pass, "10.0.0.1", base64.StdEncoding.EncodeToString([]byte(in.User + ":" + in.Pass))} {
		if strings.Contains(script, secret) {
			t.Fatalf("script contains %q", secret)
		}
	}
	if script != probeScript() {
		t.Fatal("the script varies with the credential")
	}
	// The script never prints the credential or the proxy it was given.
	for _, bad := range []string{"console.log(c.", "console.log(auth", "console.error", "stderr"} {
		if strings.Contains(script, bad) {
			t.Fatalf("script contains %q", bad)
		}
	}
}

// Neither the password, the user nor the Basic header may appear in any error,
// including one produced from an exec error that quotes them.
func TestProbeErrorsNeverCarryTheCredential(t *testing.T) {
	leaky := func(label string, out string, execErr func(in probeIn) error) {
		t.Run(label, func(t *testing.T) {
			h := newHarness(t, nil)
			var in probeIn
			err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
				func(_ context.Context, _, stdin string) (string, error) {
					in = parseIn(t, stdin)
					if execErr != nil {
						return "", execErr(in)
					}
					return out, nil
				})
			if err == nil {
				t.Fatal("expected failure")
			}
			b64 := base64.StdEncoding.EncodeToString([]byte(in.User + ":" + in.Pass))
			for _, secret := range []string{in.Pass, in.User, b64} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks %q: %v", secret, err)
				}
			}
		})
	}
	leaky("exec error quoting the stdin", "", func(in probeIn) error {
		return errors.New("docker exec: bad input " + in.User + ":" + in.Pass + " Basic " + base64.StdEncoding.EncodeToString([]byte(in.User+":"+in.Pass)))
	})
	for name, out := range map[string]string{
		"lines quoting the password": "DIRECT BLOCKED PASSWORD\nDNS BLOCKED x\nALLOW 200\nDENY 403\n",
		"allow refused":              "DIRECT BLOCKED ENETUNREACH\nDNS BLOCKED EAI_AGAIN\nALLOW 407\nDENY 403\n",
		"proxy saw nothing":          goodLines,
	} {
		leaky(name, out, nil)
	}
}

func TestProbeHonoursTheDeadline(t *testing.T) {
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })
	h := newHarness(t, nil)
	start := time.Now()
	err := Probe(context.Background(), h.p, "box-1", ProbeTarget{ProxyHost: "10.0.0.1", Port: 9072},
		func(ctx context.Context, _, _ string) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return goodLines, nil
			}
		})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("probe did not time out: err=%v after %v", err, time.Since(start))
	}
}
