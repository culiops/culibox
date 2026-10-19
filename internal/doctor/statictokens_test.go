package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
)

func TestDoctorStaticTokens(t *testing.T) {
	const secret = "tok-SECRET-1234"
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	loose := filepath.Join(dir, "loose")
	for p, mode := range map[string]os.FileMode{good: 0o600, loose: 0o644} {
		if err := os.WriteFile(p, []byte(secret+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	egress := []config.TokenEgress{{Host: "api.example.com"}}
	c := config.Config{StaticTokens: []config.StaticToken{
		{Name: "good", Value: config.KeyRef{Type: "file", Path: good}, Egress: egress},
		{Name: "loose", Value: config.KeyRef{Type: "file", Path: loose}, Egress: egress},
		{Name: "unset", Value: config.KeyRef{Type: "env", Var: "ABSENT_TOKEN"}, Egress: egress},
		{Name: "set", Value: config.KeyRef{Type: "env", Var: "PRESENT_TOKEN"}, Egress: egress},
		{Name: "wide", Value: config.KeyRef{Type: "file", Path: good}, Egress: []config.TokenEgress{
			{Host: "jira.corp.example", Private: true, CIDR: "10.0.0.0/8"},
			{Host: "narrow.corp.example", Private: true, CIDR: "10.1.2.0/24"},
			{Host: "v6.corp.example", Private: true, CIDR: "fd00:1::/48"},
		}},
	}}
	r := credref.Default{LookupEnv: func(k string) (string, bool) {
		if k == "PRESENT_TOKEN" {
			return secret, true
		}
		return "", false
	}}
	got := map[string][]Result{}
	var all strings.Builder
	for _, res := range checkStaticTokens(c, r) {
		got[res.Check] = append(got[res.Check], res)
		all.WriteString(res.Status + " " + res.Check + " " + res.Detail + "\n")
	}
	if strings.Contains(all.String(), secret) {
		t.Fatalf("a result contains the token value:\n%s", all.String())
	}
	one := func(check, status, sub string) {
		t.Helper()
		rs := got[check]
		if len(rs) == 0 {
			t.Fatalf("no result for %q:\n%s", check, all.String())
		}
		if rs[0].Status != status || !strings.Contains(rs[0].Detail, sub) {
			t.Errorf("%s: got %s %q, want %s containing %q", check, rs[0].Status, rs[0].Detail, status, sub)
		}
	}
	one("token good", Pass, "file "+good+" resolves")
	one("token loose", Fail, "mode")
	one("token unset", Warn, "ABSENT_TOKEN is not set in this shell; the daemon reads its own environment")
	one("token set", Pass, "env PRESENT_TOKEN resolves")
	if n := len(got["token wide"]); n != 2 {
		t.Fatalf("token wide: want a resolve line and exactly one wide-cidr WARN, got %d:\n%s", n, all.String())
	}
	one("token wide", Pass, "resolves")
	w := got["token wide"][1]
	if w.Status != Warn || !strings.Contains(w.Detail, "wide") || !strings.Contains(w.Detail, "10.0.0.0/8") ||
		!strings.Contains(w.Detail, "wide: private cidr") {
		t.Errorf("wide cidr warn: %+v", w)
	}
}

func TestWideTokenCIDR(t *testing.T) {
	for _, tc := range []struct {
		cidr string
		wide bool
	}{
		{"10.0.0.0/8", true}, {"172.16.0.0/12", true}, {"10.1.0.0/16", false}, {"10.1.2.0/24", false},
		{"192.168.0.0/16", false}, {"fc00::/7", true}, {"fd00::/32", true}, {"fd00:1::/48", false}, {"fd00:1:2::/64", false},
		{"not-a-cidr", false},
	} {
		if got := wideTokenCIDR(tc.cidr); got != tc.wide {
			t.Errorf("wideTokenCIDR(%q) = %v, want %v", tc.cidr, got, tc.wide)
		}
	}
}

func TestDoctor_ListsStaticTokens(t *testing.T) {
	in := input(t)
	in.Config.StaticTokens = []config.StaticToken{{
		Name: "t1", Value: config.KeyRef{Type: "env", Var: "NO_SUCH_VAR_FOR_DOCTOR"},
		Env: []string{"T1_TOKEN"}, Egress: []config.TokenEgress{{Host: "api.example.com"}},
	}}
	if err := in.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	out, _ := run(in)
	if !strings.Contains(out, "token t1") || !strings.Contains(out, "NO_SUCH_VAR_FOR_DOCTOR is not set in this shell") {
		t.Errorf("doctor output lacks the token line:\n%s", out)
	}
}
