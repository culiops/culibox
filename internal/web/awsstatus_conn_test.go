package web

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/ssologin"
)

// statusKeys marshals a status reply and returns its object keys, sorted, at
// the top level and under the named sub-objects.
func statusKeys(t *testing.T, v any, sub ...string) map[string][]string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]json.RawMessage) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	out := map[string][]string{"": keys(top)}
	for _, s := range sub {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(top[s], &m); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		out[s] = keys(m)
	}
	return out
}

// fullAdapter wires every optional callback so the reply carries every object
// a session can have.
func fullAdapter(info map[string]session.Info) AWSStatusAdapter {
	ad := awsAdapter(info, nil, nil, nil)
	ad.Egress = func(string) (egress.Stats, bool) { return egress.Stats{Allowed: 1}, true }
	ad.Elevation = func(string) (broker.ElevationView, bool) {
		return broker.ElevationView{Active: true, Until: time.Unix(1700000900, 0).UTC()}, true
	}
	ad.SSO = func(string) (SSOStatus, bool) { return SSOStatus{Expired: true, Source: "k", Command: "c"}, true }
	ad.Grant = func(string) GrantContext { return GrantContext{} }
	return ad
}

// A session with the inline binding answers with exactly the keys it had before
// connections existed: nothing connection-shaped leaks into the legacy reply.
func TestAWSStatusLegacyShapeUnchanged(t *testing.T) {
	info := map[string]session.Info{"claude-3": {ID: "claude-3", Egress: "enforce",
		AWS: &session.AWSInfo{Label: "sandbox", AccountID: "123456789012",
			RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent",
			Elevate: &session.ElevateInfo{RoleARN: "arn:aws:iam::123456789012:role/w", MaxMinutes: 15}}}}
	st, ok := fullAdapter(info).AWSStatus("claude-3")
	if !ok {
		t.Fatal("no status")
	}
	got := statusKeys(t, st, "stats", "elevate", "elevation", "sso", "egress")
	want := map[string][]string{
		"":          {"account_id", "egress", "elevate", "elevation", "label", "prod", "recent", "role_arn", "sso", "stats"},
		"stats":     {"cache_hits", "last_error", "last_mint", "mints"},
		"elevate":   {"allow_iam", "host_sessions", "max_minutes", "role_arn"},
		"elevation": {"active", "until"},
		"sso":       {"command", "expired", "source"},
		"egress":    {"allowed", "denied", "mode"},
	}
	if g, w := got, want; len(g) != len(w) {
		t.Fatalf("keys %v, want %v", g, w)
	} else {
		for k := range w {
			if !equalStrings(g[k], w[k]) {
				t.Errorf("%q keys = %v, want %v", k, g[k], w[k])
			}
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func connSessionInfo() session.Info {
	return session.Info{ID: "claude-5", Egress: "enforce", AWSConnections: []session.AWSConnInfo{
		{Name: "sbx-a", Label: "sandbox A", AccountID: "123456789012", Region: "ap-southeast-1",
			RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent", WriteCapable: true,
			Elevate: &session.ElevateInfo{RoleARN: "arn:aws:iam::123456789012:role/w", MaxMinutes: 15}},
		{Name: "sbx-b", Label: "sandbox B", AccountID: "210987654321", Region: "us-east-1",
			RoleARN: "arn:aws:iam::210987654321:role/agentbox-agent", Prod: true},
	}}
}

func connAdapter(info session.Info) AWSStatusAdapter {
	ad := fullAdapter(map[string]session.Info{info.ID: info})
	ad.ConnStats = func(id string) map[string]broker.Stats {
		if id != info.ID {
			return nil
		}
		return map[string]broker.Stats{
			"sbx-a": {Mints: 3, CacheHits: 4, LastMint: time.Unix(1700000000, 0).UTC()},
			"sbx-b": {Mints: 1, LastError: "denied_scope"},
		}
	}
	ad.Elevation = func(string) (broker.ElevationView, bool) {
		return broker.ElevationView{Active: true, Until: time.Unix(1700000900, 0).UTC(), Name: "sbx-a"}, true
	}
	ad.SSO = func(string) (SSOStatus, bool) {
		return SSOStatus{Expired: true, Source: "k", Sources: []SSOSource{{Source: "k", Names: []string{"sbx-b"}, Expired: true}}}, true
	}
	ad.Grant = func(string) GrantContext {
		return GrantContext{ConnBlocked: map[string]string{"sbx-a": "drop sbx-b first"}}
	}
	return ad
}

type connStatusWire struct {
	Connections []struct {
		Name         string `json:"name"`
		Label        string `json:"label"`
		AccountID    string `json:"account_id"`
		RoleARN      string `json:"role_arn"`
		Region       string `json:"region"`
		Prod         bool   `json:"prod"`
		WriteCapable bool   `json:"write_capable"`
		Stats        struct {
			Mints     int    `json:"mints"`
			CacheHits int    `json:"cache_hits"`
			LastError string `json:"last_error"`
		} `json:"stats"`
	} `json:"connections"`
	Elevate struct {
		ConnBlocked map[string]string `json:"conn_blocked"`
		HostSession []HostSession     `json:"host_sessions"`
	} `json:"elevate"`
	Elevation struct {
		Name string `json:"name"`
	} `json:"elevation"`
	SSO struct {
		Sources []SSOSource `json:"sources"`
	} `json:"sso"`
	Label *string `json:"label"`
}

func TestAWSStatusConnections(t *testing.T) {
	st, ok := connAdapter(connSessionInfo()).AWSStatus("claude-5")
	if !ok {
		t.Fatal("a connections session reported no status")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var got connStatusWire
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Connections) != 2 || got.Connections[0].Name != "sbx-a" || got.Connections[1].Name != "sbx-b" {
		t.Fatalf("connections %+v", got.Connections)
	}
	a, b := got.Connections[0], got.Connections[1]
	if a.Stats.Mints != 3 || a.Stats.CacheHits != 4 || b.Stats.Mints != 1 || b.Stats.LastError != "denied_scope" {
		t.Fatalf("per-connection stats %+v / %+v", a.Stats, b.Stats)
	}
	if a.AccountID != "123456789012" || a.Region != "ap-southeast-1" || !a.WriteCapable || !b.Prod {
		t.Fatalf("connection fields %+v / %+v", a, b)
	}
	if got.Label != nil {
		t.Fatal("a connections session carries no inline label")
	}
	if got.Elevation.Name != "sbx-a" {
		t.Fatalf("elevation %+v", got.Elevation)
	}
	if len(got.SSO.Sources) != 1 || got.SSO.Sources[0].Names[0] != "sbx-b" {
		t.Fatalf("sso %+v", got.SSO)
	}
	if got.Elevate.ConnBlocked["sbx-a"] != "drop sbx-b first" {
		t.Fatalf("elevate %+v", got.Elevate)
	}
	// A connection with no counters yet still reads as zeros, with stats present.
	ad := connAdapter(connSessionInfo())
	ad.ConnStats = nil
	st, _ = ad.AWSStatus("claude-5")
	raw, _ = json.Marshal(st)
	var none connStatusWire
	if err := json.Unmarshal(raw, &none); err != nil || len(none.Connections) != 2 {
		t.Fatalf("without ConnStats: %v %s", err, raw)
	}
	for _, s := range []string{"source_profile", "SourceProfile"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("%s leaked: %s", s, raw)
		}
	}
}

// A session whose only binding is named connections is bound: it gets a status,
// not the 204 that an unbound session gets.
func TestAWSStatusConnOnlySessionNot204(t *testing.T) {
	info := connSessionInfo()
	ad := awsAdapter(map[string]session.Info{info.ID: info, "claude-6": {ID: "claude-6"}}, nil, nil, nil)
	if _, ok := ad.AWSStatus("claude-5"); !ok {
		t.Fatal("a connections-only session was treated as unbound")
	}
	if _, ok := ad.AWSStatus("claude-6"); ok {
		t.Fatal("an unbound session got a status")
	}
}

// A token caller never sees a device code or URL, at the top level or under
// any per-source login; a browser session still does. The state the SSO
// callback returned is not changed in place.
func TestAWSStatusRedactsPerSourceSSOForTokens(t *testing.T) {
	login := &ssologin.State{State: "pending", URI: "https://device.example/x", URITrusted: true, Code: "ABCD-EFGH"}
	srcLogin := &ssologin.State{State: "pending", URI: "https://device.example/y", URITrusted: true, Code: "WXYZ-1234"}
	given := SSOStatus{Expired: true, Source: "k", Login: login, Sources: []SSOSource{
		{Source: "k", Names: []string{"sbx-b"}, Expired: true, Login: srcLogin},
		{Source: "j", Names: []string{"sbx-a"}},
	}}
	ad := connAdapter(connSessionInfo())
	ad.SSO = func(string) (SSOStatus, bool) { return given, true }

	st, _ := ad.AWSStatusFor("claude-5", false)
	raw, _ := json.Marshal(st)
	for _, secret := range []string{"ABCD-EFGH", "WXYZ-1234", "device.example"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("a token caller can read %q: %s", secret, raw)
		}
	}
	var wire struct {
		SSO struct {
			Sources []struct {
				Login *ssologin.State `json:"login"`
			} `json:"sources"`
		} `json:"sso"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.SSO.Sources[0].Login == nil || wire.SSO.Sources[0].Login.State != "pending" {
		t.Fatalf("redaction dropped the flow state: %v %s", err, raw)
	}
	if login.Code != "ABCD-EFGH" || srcLogin.Code != "WXYZ-1234" || given.Sources[0].Login != srcLogin ||
		srcLogin.URI == "" || login.URI == "" {
		t.Fatal("the callback's SSO state was mutated in place")
	}

	st, _ = ad.AWSStatusFor("claude-5", true)
	raw, _ = json.Marshal(st)
	for _, secret := range []string{"ABCD-EFGH", "WXYZ-1234"} {
		if !strings.Contains(string(raw), secret) {
			t.Fatalf("a browser session lost %q: %s", secret, raw)
		}
	}
}

// The status overlays the write window and the SSO state on the right
// connection only, and the top-level elevate of a connections session carries
// no inline-binding fields.
func TestAWSStatusConnectionElevationAndSSOState(t *testing.T) {
	st, ok := connAdapter(connSessionInfo()).AWSStatus("claude-5")
	if !ok {
		t.Fatal("no status")
	}
	raw, _ := json.Marshal(st)
	var wire struct {
		Connections []struct {
			Name          string     `json:"name"`
			ElevatedUntil *time.Time `json:"elevated_until"`
			SSOExpired    string     `json:"sso_expired"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	a, b := wire.Connections[0], wire.Connections[1]
	if a.Name != "sbx-a" || a.ElevatedUntil == nil || !a.ElevatedUntil.Equal(time.Unix(1700000900, 0)) || a.SSOExpired != "" {
		t.Fatalf("sbx-a %+v", a)
	}
	if b.Name != "sbx-b" || b.ElevatedUntil != nil || b.SSOExpired != "k" {
		t.Fatalf("sbx-b %+v", b)
	}
	if got := statusKeys(t, st, "elevate")["elevate"]; !equalStrings(got, []string{"conn_blocked", "host_sessions"}) {
		t.Fatalf("connections session elevate keys %v", got)
	}

	// An inactive view, or one for a session that has exited, marks nothing.
	ad := connAdapter(connSessionInfo())
	ad.Elevation = func(string) (broker.ElevationView, bool) {
		return broker.ElevationView{Active: false, LastValid: time.Unix(1700000900, 0), Name: "sbx-a"}, true
	}
	st, _ = ad.AWSStatus("claude-5")
	raw, _ = json.Marshal(st)
	if strings.Contains(string(raw), "elevated_until") {
		t.Fatalf("an inactive window marked a connection: %s", raw)
	}
}
