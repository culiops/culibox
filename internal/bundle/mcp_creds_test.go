package bundle

import (
	"strings"
	"testing"
)

// A server that authenticates with a literal credential in its headers, args or
// url would carry that credential into every boxed bundle, where the agent can
// read it. Same rule as literal env: refuse, by shape.
func TestMCPServersCarryingCredentialsAreRefused(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{
      "hdr-bearer":    {"type":"http","url":"https://example.invalid/mcp","headers":{"Authorization":"Bearer zz-literal-1"}},
      "hdr-other":     {"url":"https://example.invalid/mcp","headers":{"X-Client":"zz-literal-2"}},
      "hdr-helper":    {"url":"https://example.invalid/mcp","headersHelper":"/home/me/bin/hdrs.sh"},
      "url-query":     {"url":"https://example.invalid/mcp?api_key=zz-literal-3"},
      "url-userinfo":  {"url":"https://me:zz-literal-4@example.invalid/mcp"},
      "arg-flag-eq":   {"command":"npx","args":["-y","pkg","--api-key=zz-literal-5"]},
      "arg-flag-next": {"command":"npx","args":["-y","pkg","--access-token","zz-literal-6"]},
      "arg-header":    {"command":"npx","args":["mcp-remote","https://x.invalid","--header","Authorization: Bearer zz-literal-7"]},
      "arg-assign":    {"command":"npx","args":["GITHUB_TOKEN=zz-literal-8","pkg"]},
      "arg-prefix":    {"command":"npx","args":["pkg","ghp_abcdefghijklmnopqrstuvwxyz0123456789"]},
      "arg-url-query": {"command":"npx","args":["mcp-remote","https://x.invalid/sse?token=zz-literal-9"]},
      "arg-hdr-eq":    {"command":"npx","args":["mcp-remote","--header=Authorization: Bearer zz-literal-10"]},
      "arg-bearer":    {"command":"npx","args":["pkg","--auth-header","Bearer zz-literal-11"]},
      "url-username":  {"url":"https://zz-literal-12@example.invalid/mcp"},
      "url-camel":     {"url":"https://mcp.example.invalid/mcp/?tavilyApiKey=zz-literal-13"},
      "url-badport":   {"url":"https://me:zz-literal-14@x.invalid:abc/mcp"},
      "url-badescape": {"url":"https://x.invalid/mcp?token=zz-literal-15%zz"},
      "url-semicolon": {"url":"https://x.invalid/mcp?a=1;token=zz-literal-16"},
      "url-fragment":  {"url":"https://x.invalid/mcp#token=zz-literal-17"},
      "url-path":      {"url":"https://mcp.example.invalid/api/mcp/s/Zz9liTeral18aBcDeFgHiJkLmNoP/mcp"},
      "url-fc":        {"url":"https://mcp.example.invalid/fc-0123456789abcdef0123456789abcdef/v2/sse"},
      "arg-camel":     {"command":"npx","args":["pkg","--accessToken","zz-literal-19"]},
      "arg-sh-c":      {"command":"sh","args":["-c","npx pkg --token zz-literal-20"]},
      "arg-sh-export": {"command":"sh","args":["-c","export API_KEY=zz-literal-21; npx pkg"]},
      "arg-hdr-assign":{"command":"npx","args":["pkg","--header=X-Api-Key=zz-literal-22"]},
      "arg-scheme":    {"command":"npx","args":["pkg","-H","Token zz-literal-23"]},
      "arg-key":       {"command":"npx","args":["pkg","--key","zz-literal-24"]},
      "arg-number":    {"command":"npx","args":["pkg","--password",123456]},
      "oauth-secret":  {"url":"https://x.invalid/mcp","oauth":{"clientId":"abc","clientSecret":"zz-literal-25"}},
      "cap-headers":   {"url":"https://x.invalid/mcp","Headers":{"X-Thing":"zz-literal-26"}},
      "cmd-secret":    {"command":"/opt/bin/run --token zz-literal-27"},
      "path-disguise": {"command":"npx","args":["pkg","--password","/zz-literal-28"]},
      "path-random":   {"command":"npx","args":["pkg","--token","~/Zz9literal29aBcDeFgHiJkLm"]},
      "bare-query":    {"url":"https://x.invalid/mcp?zz-literal-30-auth"},
      "server-url":    {"command":"npx","args":["pkg"],"serverUrl":"https://x.invalid/mcp?apiKey=zz-literal-31"},
      "q-accesskey":   {"url":"https://x.invalid/mcp?accesskey=zz-literal-32"},
      "q-session":     {"url":"https://x.invalid/mcp?session=zz-literal-33"},
      "q-escaped-eq":  {"url":"https://x.invalid/mcp?apikey%3Dzz-literal-34=1"},
      "path-b64":      {"url":"https://x.invalid/s/Xk9_aQ2mPz7Lw-4RtY8nB3vC_hJ6dF1sG0eK5uQ/sse"},
      "lead-space":    {"command":"npx","args":["pkg"],"oauth":{"tokenEndpoint":" https://u:zz-literal-35@x.invalid"}},
      "token-key":     {"url":"https://x.invalid/mcp","oauth":{"sk-abcdefghijklmnopqrstuv":""}},
      "hdr-line-key":  {"url":"https://x.invalid/mcp","headers":{"Authorization: Bearer zz-literal-36":"1"}},
      "q-name-value":  {"url":"https://x.invalid/mcp?token_Zz9literal37aBcDeFgHiJkLm=1"},
      "top-token-key": {"url":"https://x.invalid/mcp","sk-abcdefghijklmnopqrstuv":"1"},
      "flag-name-val": {"command":"npx","args":["pkg","--token-Zz9literal38aBcDeFgHiJkLm","x"]}
    }}`)
	kept, drops := filterMCP(path, fakeProber{"npx": true, "sh": true})
	if len(kept) != 0 {
		t.Fatalf("every server carries a credential and must be dropped; kept %v", keys(kept))
	}
	if len(drops) != 45 {
		t.Fatalf("want 45 drops, got %d: %+v", len(drops), drops)
	}
	// The reason is shown in the UI and the lock file: it must never echo the
	// credential it is refusing to copy.
	for _, d := range drops {
		if d.Reason == "" {
			t.Errorf("drop %q has no reason", d.Name)
		}
		if strings.Contains(d.Reason, "zz-literal") || strings.Contains(d.Reason, "ghp_") || strings.Contains(d.Reason, "sk-") || strings.Contains(d.Reason, "Zz9literal") {
			t.Errorf("drop %q reason leaks the value: %q", d.Name, d.Reason)
		}
	}
}

// Ordinary servers must survive: a credential rule that drops playwright or a
// filesystem server would be switched off by the first person it annoyed.
func TestMCPServersWithoutCredentialsAreKept(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{
      "playwright":  {"command":"npx","args":["@playwright/mcp@latest","--headless","--browser","chromium"],"env":{}},
      "fs":          {"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/work"]},
      "remote":      {"type":"http","url":"https://example.invalid/mcp?region=eu"},
      "empty-hdrs":  {"url":"https://example.invalid/mcp","headers":{}},
      "token-limit": {"command":"npx","args":["pkg","--max-tokens","4096"]},
      "bool-flag":   {"command":"npx","args":["pkg","--no-auth"]},
      "origins":     {"command":"npx","args":["@playwright/mcp","--allowed-origins=https://a.invalid,https://b.invalid"]},
      "tok-limit":   {"command":"npx","args":["pkg","--token-limit","4096","--token-type","bearer"]},
      "pw-file":     {"command":"npx","args":["pkg","--password-file","/run/secrets/db","--credentials","~/adc.json"]},
      "cookie-jar":  {"command":"npx","args":["pkg","--cookie-jar","/tmp/c"]},
      "uuid-path":   {"url":"https://x.invalid/mcp/123e4567-e89b-12d3-a456-426614174000/sse"},
      "oauth-plain": {"url":"https://x.invalid/mcp","oauth":{"clientId":"abc","callbackPort":8080,"tokenEndpoint":"https://x.invalid/t"}},
      "author-q":    {"url":"https://x.invalid/mcp?author=me&keyword=x&design=y"},
      "slug-path":   {"url":"https://gitmcp.io/culiops/agent-box-v2-2026-release/projects/acme-production-cluster-01"},
      "sha-arg":     {"command":"npx","args":["pkg","https://raw.githubusercontent.com/o/r/0123456789abcdef0123456789abcdef01234567/x.json"]},
      "auth-mode":   {"command":"npx","args":["pkg","--auth","oauth"]}
    }}`)
	kept, drops := filterMCP(path, fakeProber{"npx": true})
	for _, want := range []string{"playwright", "fs", "remote", "empty-hdrs", "token-limit", "bool-flag",
		"origins", "tok-limit", "pw-file", "cookie-jar", "uuid-path", "oauth-plain",
		"author-q", "slug-path", "sha-arg", "auth-mode"} {
		if _, ok := kept[want]; !ok {
			t.Errorf("%s should have been kept; drops=%+v", want, drops)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A whitespace-only command is "no command", not a crash: one stray value in
// the user's config must not panic every bundle build.
func TestWhitespaceCommandIsDroppedNotPanicking(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{"blank":{"command":"   "}}}`)
	kept, drops := filterMCP(path, fakeProber{"npx": true})
	if len(kept) != 0 || len(drops) != 1 {
		t.Fatalf("kept=%v drops=%+v", kept, drops)
	}
}

// The missing-command reason names only a plain base name.
func TestMissingCommandReasonWithholdsOddNames(t *testing.T) {
	path := writeMCP(t, `{"mcpServers":{"s":{"command":"/opt/Zz9literal39aBcDeFgHiJkLm/run"},"p":{"command":"/usr/local/bin/thing"}}}`)
	_, drops := filterMCP(path, DenyAllProber{})
	for _, d := range drops {
		if strings.Contains(d.Reason, "Zz9literal") || strings.Contains(d.Reason, "/usr/local") {
			t.Errorf("reason %q echoes more than a plain base name", d.Reason)
		}
	}
}
