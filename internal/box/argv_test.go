package box

import (
	"strings"
	"testing"
)

// The store key is protected from a box by Docker's default seccomp profile
// (it blocks keyctl). No argv this package builds may weaken that.
func TestBoxArgvNeverDisablesSeccompOrPrivileges(t *testing.T) {
	m := newTestManager(t)
	var all [][]string
	for _, fenced := range []bool{false, true} {
		for _, excl := range []bool{false, true} {
			all = append(all, m.runArgs("ab-box-x", "/w", []Mount{{Host: "/h", Box: "/b"}}, fenced, excl, "", DefaultMemory, ""))
		}
	}
	all = append(all, m.execArgv("ab-box-x", "/w", "sid", []string{"claude"}, []string{"A=1"}))
	for _, argv := range all {
		j := strings.Join(argv, " ")
		for _, bad := range []string{"seccomp=unconfined", "--privileged", "apparmor=unconfined"} {
			if strings.Contains(j, bad) {
				t.Fatalf("box argv contains %q: %s", bad, j)
			}
		}
	}
}

// The out dir is how the boxed Playwright MCP's screenshots reach the host:
// writable (the box writes into it) and keyed by the box name, outside the home.
func TestRunArgsMountsOutputDir(t *testing.T) {
	m := newTestManager(t)
	got := m.runArgs(outTestBox, "/w", nil, false, false, "", DefaultMemory, "")
	want := "type=bind,src=" + OutputDir(m.opts.StateRoot, outTestBox) + ",dst=/tmp/playwright-mcp"
	if !containsPair(got, "--mount", want) {
		t.Fatalf("missing writable out dir mount %q in %q", want, strings.Join(got, " "))
	}
}
