package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	boximage "github.com/culiops/agent-box/docker/agent-box"
	"github.com/culiops/agent-box/internal/extraca"
)

// hintRoot dials the failing host for the interception hint. A var so the
// tests do not touch the network.
var hintRoot = extraca.RootSubject

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
// output (spec 6.7). It prints nothing for any other output. The chain comes
// from a host-side dial to the host the output itself names: there is no
// fallback host, because interception can be selective by domain and a guess
// would show an untouched chain. If no host parses or the dial fails, the
// chain and export lines are left out; the hint never blocks or retries.
func printInterceptionHint(ctx context.Context, w io.Writer, output string, configured bool,
	root func(context.Context, string) (string, string, error)) {
	if !extraca.Intercepted(output) {
		return
	}
	rerun := "`agent-box box setup -rebuild`"
	if runtime.GOOS == "darwin" {
		rerun = "`agent-box box setup -recreate`"
	}
	fmt.Fprintln(w, "HINT: HTTPS inside the box VM/build is being intercepted by a TLS-inspecting proxy.")
	if configured {
		fmt.Fprintln(w, `      The configured CAs did not cover this chain. Export the root CA shown below, put it in`)
		fmt.Fprintf(w, "      \"extra_ca_certs\" in config.json, then run %s.\n", rerun)
	} else {
		fmt.Fprintln(w, `      Export its root CA and add it to "extra_ca_certs" in config.json, then run`)
		fmt.Fprintf(w, "      %s.\n", rerun)
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
	if cn != "" {
		fmt.Fprintf(w, "      macOS export: security find-certificate -c %q -p > ~/agent-box-ca.pem\n", cn)
	}
}

// hintBuildFailure is the hint for a failed Build: the kept tail of its output.
func hintBuildFailure(ctx context.Context, w io.Writer, d boximage.Docker, err error) {
	var oe *boximage.OutputError
	if !errors.As(err, &oe) {
		return
	}
	printInterceptionHint(ctx, w, oe.Tail, len(d.ExtraCA) > 0, hintRoot)
}

// hintPullFailure is the hint for a failed Pull, whose error carries docker's output.
func hintPullFailure(ctx context.Context, w io.Writer, d boximage.Docker, err error) {
	printInterceptionHint(ctx, w, err.Error(), len(d.ExtraCA) > 0, hintRoot)
}
