// Package credtemplates is the provider catalog behind the credentials screen's
// "Add token" tiles. It is code, not config: a small read-only table that only
// prefills the form. Build turns a template and its fields into token entries;
// StaticToken.Validate (run again by the credential manager) stays the
// authority on what is accepted.
//
// Every env name and host below was checked against the provider's own docs
// (URL in the comment above each template, checked 2026-10-20). Catalog
// entries are public egress only: a web-defined token never carries a private
// host.
package credtemplates

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/hostmatch"
)

// Option is one choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field is one input a template asks for. Kind is select, url or text.
type Field struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	Options []Option `json:"options,omitempty"`
}

// Template is one provider tile. Values holds one label per secret the template
// needs (Datadog needs two). Group is where the card is listed: tokens or github.
type Template struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Label    string   `json:"label"`
	Group    string   `json:"group"`
	Fields   []Field  `json:"fields"`
	Values   []string `json:"values"`
}

var (
	sentryRegions = []Option{{"us", "US (us.sentry.io)"}, {"eu", "EU (de.sentry.io)"}, {"self", "Self-hosted"}}

	// Datadog site parameters. api.<site> is the API host for each.
	datadogSites = []Option{
		{"datadoghq.com", "US1 (datadoghq.com)"},
		{"us3.datadoghq.com", "US3 (us3.datadoghq.com)"},
		{"us5.datadoghq.com", "US5 (us5.datadoghq.com)"},
		{"datadoghq.eu", "EU (datadoghq.eu)"},
		{"ap1.datadoghq.com", "AP1 (ap1.datadoghq.com)"},
		{"ap2.datadoghq.com", "AP2 (ap2.datadoghq.com)"},
		{"uk1.datadoghq.com", "UK1 (uk1.datadoghq.com)"},
	}

	pagerdutyRegions = []Option{{"us", "US"}, {"eu", "EU"}}
)

// catalog is in tile order.
//
// Docs checked per template:
//
// sentry
//   - env SENTRY_AUTH_TOKEN, SENTRY_URL (self-hosted):
//     https://docs.sentry.io/cli/configuration/
//   - region hosts (us.sentry.io, de.sentry.io; sentry.io is the default host):
//     https://docs.sentry.io/api/
//
// grafana
//   - GRAFANA_URL, GRAFANA_SERVICE_ACCOUNT_TOKEN:
//     https://github.com/grafana/mcp-grafana (Grafana's own MCP server README)
//
// datadog
//   - DD_API_KEY, DD_APP_KEY, DD_SITE (the site parameter, e.g. datadoghq.eu):
//     https://docs.datadoghq.com/security/code_security/secret_scanning/generic_ci_providers/
//   - site parameters: https://docs.datadoghq.com/getting_started/site/
//   - API host per site (api.<site>): https://docs.datadoghq.com/api/latest/authentication/
//
// pagerduty (DIFFERS from the spec table, which had PAGERDUTY_API_TOKEN: the
// provider's own tool reads PAGERDUTY_USER_API_KEY, and PAGERDUTY_API_HOST for EU)
//   - https://github.com/PagerDuty/pagerduty-mcp-server/blob/main/docs/configuration/environment-variables.md
//   - API hosts api.pagerduty.com / api.eu.pagerduty.com:
//     https://support.pagerduty.com/docs/service-regions
//
// github-pat
//   - GH_TOKEN (applies to github.com): https://cli.github.com/manual/gh_help_environment
//     api.github.com is the REST host, github.com the git host.
var catalog = []Template{
	{ID: "sentry", Provider: "sentry", Label: "Sentry", Group: "tokens", Values: []string{"Auth token"},
		Fields: []Field{
			{Key: "region", Label: "Region", Kind: "select", Options: sentryRegions},
			{Key: "url", Label: "Self-hosted URL (https://sentry.example.com)", Kind: "url"},
		}},
	{ID: "grafana", Provider: "grafana", Label: "Grafana", Group: "tokens", Values: []string{"Service account token"},
		Fields: []Field{{Key: "url", Label: "Stack URL (https://<stack>.grafana.net)", Kind: "url"}}},
	{ID: "datadog", Provider: "datadog", Label: "Datadog", Group: "tokens", Values: []string{"API key", "Application key"},
		Fields: []Field{{Key: "site", Label: "Site", Kind: "select", Options: datadogSites}}},
	{ID: "pagerduty", Provider: "pagerduty", Label: "PagerDuty", Group: "tokens", Values: []string{"User API token"},
		Fields: []Field{{Key: "region", Label: "Service region", Kind: "select", Options: pagerdutyRegions}}},
	{ID: "github-pat", Provider: "github", Label: "GitHub personal access token", Group: "github", Values: []string{"Personal access token"}},
	{ID: "other", Provider: "other", Label: "Other", Group: "tokens", Values: []string{"Token"},
		Fields: []Field{
			{Key: "provider", Label: "Provider name", Kind: "text"},
			{Key: "env", Label: "Variable name(s), comma separated", Kind: "text"},
			{Key: "hosts", Label: "Host(s), comma separated (host or host:port)", Kind: "text"},
		}},
}

// All returns the catalog. The caller may modify the result.
func All() []Template {
	out := make([]Template, len(catalog))
	for i, t := range catalog {
		t.Fields = slices.Clone(t.Fields)
		for j := range t.Fields {
			t.Fields[j].Options = slices.Clone(t.Fields[j].Options)
		}
		t.Values = slices.Clone(t.Values)
		if t.Fields == nil {
			t.Fields = []Field{}
		}
		out[i] = t
	}
	return out
}

// Known reports whether id is a catalog template id.
func Known(id string) bool {
	return slices.ContainsFunc(catalog, func(t Template) bool { return t.ID == id })
}

func find(id string) (Template, bool) {
	for _, t := range catalog {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// Build makes the token entries for a template. The entries carry Provider,
// Template (the catalog id; empty for "other", which stays fully editable),
// Env, ConfigEnv and public Egress. Value is left empty: the credential manager
// fills {type: store, id} when it stores the secret. Datadog returns two entries,
// <name>-api and <name>-app. Build does not run StaticToken.Validate (there is
// no value yet); the manager does, on the merged set.
func Build(id, name, label string, prod bool, fields map[string]string) ([]config.StaticToken, error) {
	t, ok := find(id)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", id)
	}
	for k := range fields {
		if !slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Key == k }) {
			return nil, fmt.Errorf("template %s has no field %q", id, k)
		}
	}
	base := config.StaticToken{Provider: t.Provider, Template: id, Label: label, Prod: prod}

	var out []config.StaticToken
	var err error
	switch id {
	case "sentry":
		out, err = buildSentry(base, name, fields)
	case "grafana":
		out, err = buildGrafana(base, name, fields)
	case "datadog":
		out, err = buildDatadog(base, name, fields)
	case "pagerduty":
		out, err = buildPagerDuty(base, name, fields)
	case "github-pat":
		e := base
		e.Name, e.Env = name, []string{"GH_TOKEN"}
		e.Egress = []config.TokenEgress{{Host: "api.github.com"}, {Host: "github.com"}}
		out = one(e, t.Label)
	case "other":
		out, err = buildOther(base, name, fields)
	default:
		err = fmt.Errorf("template %q has no builder", id)
	}
	if err != nil {
		return nil, err
	}
	// A host that is an IP address or not a plain DNS name never reaches the
	// manager: say so next to the form field it came from.
	for _, e := range out {
		for _, h := range e.Egress {
			if n, ok := hostmatch.Normalize(h.Host); !ok || n != h.Host {
				return nil, fmt.Errorf("host %q must be a DNS name (no IP address, no wildcard)", h.Host)
			}
		}
	}
	return out, nil
}

// one finishes a single-entry template: an empty label becomes the default.
func one(e config.StaticToken, def string) []config.StaticToken {
	if e.Label == "" {
		e.Label = def
	}
	return []config.StaticToken{e}
}

func choice(opts []Option, v, what string) error {
	if !slices.ContainsFunc(opts, func(o Option) bool { return o.Value == v }) {
		return fmt.Errorf("%s: choose one of the listed options", what)
	}
	return nil
}

func buildSentry(base config.StaticToken, name string, f map[string]string) ([]config.StaticToken, error) {
	e := base
	e.Name, e.Env = name, []string{"SENTRY_AUTH_TOKEN"}
	region := f["region"]
	if err := choice(sentryRegions, region, "region"); err != nil {
		return nil, err
	}
	switch region {
	case "us":
		e.Egress = []config.TokenEgress{{Host: "sentry.io"}, {Host: "us.sentry.io"}}
	case "eu":
		e.Egress = []config.TokenEgress{{Host: "sentry.io"}, {Host: "de.sentry.io"}}
	case "self":
		h, port, selfURL, err := httpsBase(f["url"])
		if err != nil {
			return nil, fmt.Errorf("self-hosted URL: %w", err)
		}
		e.Egress = []config.TokenEgress{{Host: h, Port: port}}
		e.ConfigEnv = map[string]string{"SENTRY_URL": selfURL}
	}
	return one(e, "Sentry"), nil
}

func buildGrafana(base config.StaticToken, name string, f map[string]string) ([]config.StaticToken, error) {
	h, port, u, err := httpsBase(f["url"])
	if err != nil {
		return nil, fmt.Errorf("stack URL: %w", err)
	}
	e := base
	e.Name, e.Env = name, []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"}
	e.ConfigEnv = map[string]string{"GRAFANA_URL": u}
	e.Egress = []config.TokenEgress{{Host: h, Port: port}}
	return one(e, "Grafana"), nil
}

func buildDatadog(base config.StaticToken, name string, f map[string]string) ([]config.StaticToken, error) {
	site := f["site"]
	if err := choice(datadogSites, site, "site"); err != nil {
		return nil, err
	}
	mk := func(suffix, env, def string) config.StaticToken {
		e := base
		e.Name, e.Env = name+suffix, []string{env}
		e.ConfigEnv = map[string]string{"DD_SITE": site}
		e.Egress = []config.TokenEgress{{Host: "api." + site}}
		if e.Label == "" {
			e.Label = def
		}
		return e
	}
	return []config.StaticToken{mk("-api", "DD_API_KEY", "Datadog API key"), mk("-app", "DD_APP_KEY", "Datadog app key")}, nil
}

func buildPagerDuty(base config.StaticToken, name string, f map[string]string) ([]config.StaticToken, error) {
	e := base
	e.Name, e.Env = name, []string{"PAGERDUTY_USER_API_KEY"}
	switch f["region"] {
	case "us":
		e.Egress = []config.TokenEgress{{Host: "api.pagerduty.com"}}
	case "eu":
		e.Egress = []config.TokenEgress{{Host: "api.eu.pagerduty.com"}}
		e.ConfigEnv = map[string]string{"PAGERDUTY_API_HOST": "https://api.eu.pagerduty.com"}
	default:
		return nil, errors.New("region: choose one of the listed options")
	}
	return one(e, "PagerDuty"), nil
}

func buildOther(base config.StaticToken, name string, f map[string]string) ([]config.StaticToken, error) {
	e := base
	e.Name, e.Template, e.Provider = name, "", strings.TrimSpace(f["provider"])
	for _, n := range splitList(f["env"]) {
		e.Env = append(e.Env, n)
	}
	if len(e.Env) == 0 {
		return nil, errors.New("env: name at least one variable")
	}
	for _, h := range splitList(f["hosts"]) {
		host, port := h, 0
		if i := strings.LastIndex(h, ":"); i >= 0 {
			p, err := strconv.Atoi(h[i+1:])
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("hosts: %q has an invalid port", h)
			}
			host, port = h[:i], p
			if port == 443 {
				port = 0
			}
		}
		e.Egress = append(e.Egress, config.TokenEgress{Host: strings.ToLower(host), Port: port})
	}
	if len(e.Egress) == 0 {
		return nil, errors.New("hosts: name at least one host")
	}
	return []config.StaticToken{e}, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		out = append(out, p)
	}
	return out
}

// httpsBase parses an https URL into its host, its port (0 for 443) and the
// base URL to hand a program: scheme, host, optional port and path, no
// trailing slash. Credentials, a query and a fragment are refused.
func httpsBase(raw string) (host string, port int, base string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, "", errors.New("required")
	}
	u, perr := url.Parse(raw)
	if perr != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", 0, "", errors.New("must be an https URL such as https://host.example.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", 0, "", errors.New("must not contain credentials, a query or a fragment")
	}
	host = strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" {
		n, aerr := strconv.Atoi(p)
		if aerr != nil || n < 1 || n > 65535 {
			return "", 0, "", errors.New("invalid port")
		}
		if n != 443 {
			port = n
		}
	}
	base = "https://" + host
	if port != 0 {
		base += ":" + strconv.Itoa(port)
	}
	base += strings.TrimRight(u.EscapedPath(), "/")
	return host, port, base, nil
}
