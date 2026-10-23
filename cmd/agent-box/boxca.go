package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/extraca"
)

// hintRoot dials the failing host for the interception hint. A var so the
// tests do not touch the network.
var hintRoot = extraca.RootSubject

// hintConfigPath is the config.json a first-time user must create, set at boot
// only when no config file was found; "" otherwise. A var, like hintRoot, so
// the many setup paths that reach the hint need no extra parameter.
var hintConfigPath string

// printExtraCA prints one line per configured CA, and nothing when none are.
// An expired or not-yet-valid CA is a WARN: it still loads, and trust fails
// only once the proxy's chain needs it.
func printExtraCA(w io.Writer, s extraca.Set, now time.Time) {
	for _, ca := range s.CAs {
		valid, line := ca.Describe(now)
		if valid {
			fmt.Fprintf(w, "PASS extra-ca: %s\n", line)
		} else {
			fmt.Fprintf(w, "WARN extra-ca: %s (expired or not yet valid)\n", line)
		}
	}
}

// printInterceptionHint explains a TLS-interception failure in a build or pull
// output (spec 6.7). It prints nothing for any other output. goos is the
// platform the boxes run on; pull says the output is from `docker pull`. The
// chain comes from a host-side dial to the host the output itself names: there
// is no fallback host, because interception can be selective by domain and a
// guess would show an untouched chain. If no host parses or the dial fails,
// the chain and export lines are left out; the hint never blocks or retries.
//
// A Linux pull goes through the host dockerd, whose trust store agent-box does
// not manage, so extra_ca_certs cannot help it and the hint says what can.
func printInterceptionHint(ctx context.Context, w io.Writer, output string, configured bool,
	goos string, pull bool, root func(context.Context, string) (string, string, error)) {
	if !extraca.Intercepted(output) {
		return
	}
	hostTrust := pull && goos != "darwin"
	rerun := "`agent-box box setup -rebuild`"
	if goos == "darwin" {
		rerun = "`agent-box box setup -recreate`"
	}
	switch {
	case hostTrust:
		fmt.Fprintln(w, "HINT: HTTPS from the host's docker daemon is being intercepted by a TLS-inspecting proxy.")
		fmt.Fprintln(w, "      agent-box does not manage the host's trust store: install the proxy's root CA there")
		fmt.Fprintln(w, "      (Debian/Ubuntu: copy it to /usr/local/share/ca-certificates/ as a .crt, run")
		fmt.Fprintln(w, "      `update-ca-certificates`), then restart docker.")
	case configured:
		fmt.Fprintln(w, "HINT: HTTPS inside the box VM/build is being intercepted by a TLS-inspecting proxy.")
		fmt.Fprintln(w, `      The configured CAs did not cover this chain. Export the root CA shown below, put it in`)
		fmt.Fprintf(w, "      \"extra_ca_certs\" in config.json, then run %s.\n", rerun)
	default:
		fmt.Fprintln(w, "HINT: HTTPS inside the box VM/build is being intercepted by a TLS-inspecting proxy.")
		fmt.Fprintln(w, `      Export its root CA and add it to "extra_ca_certs" in config.json, then run`)
		fmt.Fprintf(w, "      %s.\n", rerun)
	}
	if !hostTrust && !configured && hintConfigPath != "" {
		// Last, after the chain and export lines: that is the order the user acts in.
		defer printNoConfigSample(w, hintConfigPath)
	}
	host := extraca.FailingHost(output)
	if host == "" {
		return
	}
	subject, cn, err := root(ctx, host)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "      The host sees this chain for %s: ... → %s\n", host, subject)
	// `security` is macOS-only.
	if cn != "" && goos == "darwin" {
		fmt.Fprintf(w, "      macOS export: security find-certificate -c %q -p > ~/agent-box-ca.pem\n", cn)
	}
}

// minimalConfig is the smallest config.json that behaves like the built-in
// defaults plus a CA. Its fields mirror Config's tags, in the order a user
// reads them: what to change (extra_ca_certs) comes last.
type minimalConfig struct {
	BindAddr       string         `json:"bind_addr"`
	DefaultRuntime string         `json:"default_runtime"`
	Agents         []minimalAgent `json:"agents"`
	ExtraCACerts   []string       `json:"extra_ca_certs"`
}

// minimalAgent is the two fields an agent needs; AgentConfig would also print
// the legacy "box": {} that omitempty cannot drop from a struct.
type minimalAgent struct {
	Name string `json:"name"`
	Bin  string `json:"bin"`
}

// printNoConfigSample is the first-run half of the interception hint: with no
// config file, "add it to extra_ca_certs in config.json" leads straight into a
// file that is rejected for having no agents and no bind_addr, because a config
// file REPLACES the built-in defaults. So it names the file to create and shows
// one that is valid once the CA path is real, taken from config.Defaults() so it
// cannot drift from them.
func printNoConfigSample(w io.Writer, path string) {
	d := config.Defaults()
	m := minimalConfig{BindAddr: d.BindAddr, DefaultRuntime: d.DefaultRuntime,
		ExtraCACerts: []string{"/absolute/path/to/agent-box-ca.pem"}}
	for _, a := range d.Agents {
		m.Agents = append(m.Agents, minimalAgent{a.Name, a.Bin})
	}
	b, err := json.MarshalIndent(m, "      ", "  ")
	if err != nil {
		return // a fixed struct cannot fail to marshal; the hint above still stands
	}
	fmt.Fprintf(w, "      No config file exists yet: create %s.\n", path)
	fmt.Fprintln(w, "      A config file replaces the built-in defaults, so it must carry them too (the values")
	fmt.Fprintln(w, "      below are the defaults). The CA path must be absolute; ~ is not expanded:")
	fmt.Fprintf(w, "      %s\n", b)
}

// hintBuildFailure is the hint for a failed Build: the kept tail of its output.
func hintBuildFailure(ctx context.Context, w io.Writer, d boximage.Docker, goos string, err error) {
	var oe *boximage.OutputError
	if !errors.As(err, &oe) {
		return
	}
	printInterceptionHint(ctx, w, oe.Tail, len(d.ExtraCA) > 0, goos, false, hintRoot)
}

// hintPullFailure is the hint for a failed Pull, whose error carries docker's output.
func hintPullFailure(ctx context.Context, w io.Writer, d boximage.Docker, goos string, err error) {
	printInterceptionHint(ctx, w, err.Error(), len(d.ExtraCA) > 0, goos, true, hintRoot)
}
