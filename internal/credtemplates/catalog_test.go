package credtemplates

import (
	"slices"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

// sample is a valid set of fields for each template.
var sample = map[string]map[string]string{
	"sentry":     {"region": "us"},
	"grafana":    {"url": "https://acme.grafana.net"},
	"datadog":    {"site": "datadoghq.eu"},
	"pagerduty":  {"region": "eu"},
	"github-pat": {},
	"other":      {"provider": "acme", "env": "ACME_TOKEN, ACME_ORG", "hosts": "api.acme.example, eu.acme.example:8443"},
}

func TestEveryTemplateBuildsValidEntries(t *testing.T) {
	all := All()
	if len(all) != len(sample) {
		t.Fatalf("catalog has %d templates, test knows %d", len(all), len(sample))
	}
	for _, tpl := range all {
		fields, ok := sample[tpl.ID]
		if !ok {
			t.Fatalf("no sample for %q", tpl.ID)
		}
		got, err := Build(tpl.ID, "mine", "My label", true, fields)
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		if len(got) != len(tpl.Values) {
			t.Errorf("%s: %d entries for %d values", tpl.ID, len(got), len(tpl.Values))
		}
		for _, e := range got {
			if e.Value != (config.KeyRef{}) {
				t.Errorf("%s: Build set a value reference %+v", tpl.ID, e.Value)
			}
			e.Value = config.KeyRef{Type: "store", ID: e.Name}
			if err := e.Validate(); err != nil {
				t.Errorf("%s: %v", tpl.ID, err)
			}
			if !e.Prod || e.Label != "My label" {
				t.Errorf("%s: prod/label not carried: %+v", tpl.ID, e)
			}
		}
		if err := config.ValidateStaticTokens(withValues(got)); err != nil {
			t.Errorf("%s: set: %v", tpl.ID, err)
		}
	}
}

func withValues(in []config.StaticToken) []config.StaticToken {
	out := slices.Clone(in)
	for i := range out {
		out[i].Value = config.KeyRef{Type: "store", ID: out[i].Name}
	}
	return out
}

func TestTemplateFieldsAreDeclared(t *testing.T) {
	for _, tpl := range All() {
		if tpl.ID == "" || tpl.Provider == "" || tpl.Label == "" || len(tpl.Values) == 0 {
			t.Errorf("incomplete template %+v", tpl)
		}
		if tpl.Group != "tokens" && tpl.Group != "github" {
			t.Errorf("%s: group %q", tpl.ID, tpl.Group)
		}
		for _, f := range tpl.Fields {
			if !slices.Contains([]string{"select", "url", "text"}, f.Kind) {
				t.Errorf("%s.%s: kind %q", tpl.ID, f.Key, f.Kind)
			}
			if (f.Kind == "select") != (len(f.Options) > 0) {
				t.Errorf("%s.%s: options do not match kind", tpl.ID, f.Key)
			}
		}
		if _, ok := sample[tpl.ID]; ok {
			for k := range sample[tpl.ID] {
				if !slices.ContainsFunc(tpl.Fields, func(f Field) bool { return f.Key == k }) {
					t.Errorf("%s: sample field %q is not declared", tpl.ID, k)
				}
			}
		}
	}
}

func TestKnown(t *testing.T) {
	if !Known("datadog") || Known("nope") || Known("") {
		t.Fatal("Known")
	}
}

func TestDatadogBuildsTwo(t *testing.T) {
	got, err := Build("datadog", "dd", "", false, map[string]string{"site": "us3.datadoghq.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "dd-api" || got[1].Name != "dd-app" {
		t.Fatalf("%+v", got)
	}
	if got[0].Env[0] != "DD_API_KEY" || got[1].Env[0] != "DD_APP_KEY" {
		t.Fatalf("env: %v %v", got[0].Env, got[1].Env)
	}
	for _, e := range got {
		if e.ConfigEnv["DD_SITE"] != "us3.datadoghq.com" {
			t.Errorf("%s: config_env %v", e.Name, e.ConfigEnv)
		}
		if len(e.Egress) != 1 || e.Egress[0].Host != "api.us3.datadoghq.com" {
			t.Errorf("%s: egress %v", e.Name, e.Egress)
		}
		if e.Template != "datadog" || e.Provider != "datadog" {
			t.Errorf("%s: %q %q", e.Name, e.Template, e.Provider)
		}
	}
	if got[0].Label == got[1].Label || got[0].Label == "" {
		t.Errorf("default labels should differ: %q %q", got[0].Label, got[1].Label)
	}
}

func TestNoPrivateEgress(t *testing.T) {
	for id, fields := range sample {
		got, err := Build(id, "mine", "", false, fields)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		for _, e := range got {
			if len(e.Egress) == 0 {
				t.Errorf("%s: no egress", id)
			}
			for _, h := range e.Egress {
				if h.Private || h.CIDR != "" {
					t.Errorf("%s: private egress %+v", id, h)
				}
			}
			if e.MCP != nil {
				t.Errorf("%s: mcp set", id)
			}
		}
	}
}

func TestSentryRegionHosts(t *testing.T) {
	hosts := func(fields map[string]string) ([]string, config.StaticToken) {
		t.Helper()
		got, err := Build("sentry", "s", "", false, fields)
		if err != nil {
			t.Fatal(err)
		}
		var hs []string
		for _, h := range got[0].Egress {
			hs = append(hs, h.Host)
		}
		return hs, got[0]
	}
	us, e := hosts(map[string]string{"region": "us"})
	if !slices.Contains(us, "us.sentry.io") || len(e.ConfigEnv) != 0 || e.Env[0] != "SENTRY_AUTH_TOKEN" {
		t.Errorf("us: %v %v", us, e.ConfigEnv)
	}
	eu, e := hosts(map[string]string{"region": "eu"})
	if !slices.Contains(eu, "de.sentry.io") || slices.Contains(eu, "us.sentry.io") || len(e.ConfigEnv) != 0 {
		t.Errorf("eu: %v", eu)
	}
	self, e := hosts(map[string]string{"region": "self", "url": "https://Sentry.Corp.Example:9000/"})
	if len(self) != 1 || self[0] != "sentry.corp.example" || e.Egress[0].Port != 9000 {
		t.Errorf("self: %+v", e.Egress)
	}
	if e.ConfigEnv["SENTRY_URL"] != "https://sentry.corp.example:9000" {
		t.Errorf("SENTRY_URL = %q", e.ConfigEnv["SENTRY_URL"])
	}
	self, e = hosts(map[string]string{"region": "self", "url": "https://sentry.corp.example"})
	if e.Egress[0].Port != 0 || e.ConfigEnv["SENTRY_URL"] != "https://sentry.corp.example" {
		t.Errorf("self 443: %+v %v", e.Egress, e.ConfigEnv)
	}
}

func TestGrafanaAndPagerDuty(t *testing.T) {
	g, err := Build("grafana", "g", "", false, map[string]string{"url": "https://acme.grafana.net/"})
	if err != nil {
		t.Fatal(err)
	}
	if g[0].Env[0] != "GRAFANA_SERVICE_ACCOUNT_TOKEN" || g[0].ConfigEnv["GRAFANA_URL"] != "https://acme.grafana.net" || g[0].Egress[0].Host != "acme.grafana.net" {
		t.Errorf("%+v", g[0])
	}
	p, err := Build("pagerduty", "p", "", false, map[string]string{"region": "us"})
	if err != nil {
		t.Fatal(err)
	}
	if p[0].Egress[0].Host != "api.pagerduty.com" || len(p[0].ConfigEnv) != 0 {
		t.Errorf("%+v", p[0])
	}
	p, _ = Build("pagerduty", "p", "", false, map[string]string{"region": "eu"})
	if p[0].Egress[0].Host != "api.eu.pagerduty.com" || p[0].ConfigEnv["PAGERDUTY_API_HOST"] != "https://api.eu.pagerduty.com" {
		t.Errorf("%+v", p[0])
	}
	gh, _ := Build("github-pat", "gh", "", false, nil)
	if gh[0].Env[0] != "GH_TOKEN" || len(gh[0].Egress) != 2 {
		t.Errorf("%+v", gh[0])
	}
}

func TestOtherIsNotTemplateLocked(t *testing.T) {
	got, err := Build("other", "o", "", false, sample["other"])
	if err != nil {
		t.Fatal(err)
	}
	e := got[0]
	if e.Template != "" || e.Provider != "acme" || !slices.Equal(e.Env, []string{"ACME_TOKEN", "ACME_ORG"}) {
		t.Fatalf("%+v", e)
	}
	if len(e.Egress) != 2 || e.Egress[1].Host != "eu.acme.example" || e.Egress[1].Port != 8443 {
		t.Fatalf("%+v", e.Egress)
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	cases := []struct {
		id     string
		fields map[string]string
	}{
		{"nope", nil},
		{"sentry", map[string]string{"region": "mars"}},
		{"sentry", map[string]string{"region": "self"}},
		{"sentry", map[string]string{"region": "self", "url": "http://plain.example"}},
		{"sentry", map[string]string{"region": "self", "url": "https://u:p@sentry.example"}},
		{"sentry", map[string]string{"region": "self", "url": "https://sentry.example/?x=1"}},
		{"sentry", map[string]string{"region": "us", "bogus": "x"}},
		{"grafana", map[string]string{"url": "https://10.0.0.5"}},
		{"datadog", map[string]string{"site": "evil.example"}},
		{"pagerduty", map[string]string{"region": ""}},
		{"github-pat", map[string]string{"x": "y"}},
		{"other", map[string]string{"provider": "a", "env": "", "hosts": "a.example"}},
		{"other", map[string]string{"provider": "a", "env": "A_B", "hosts": ""}},
		{"other", map[string]string{"provider": "a", "env": "A_B", "hosts": "a.example:99999"}},
	}
	for _, c := range cases {
		if _, err := Build(c.id, "n", "", false, c.fields); err == nil {
			t.Errorf("%s %v: no error", c.id, c.fields)
		}
	}
}
