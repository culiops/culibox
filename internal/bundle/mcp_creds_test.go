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
      "url-username":  {"url":"https://zz-literal-12@example.invalid/mcp"}
    }}`)
	kept, drops := filterMCP(path, fakeProber{"npx": true})
	if len(kept) != 0 {
		t.Fatalf("every server carries a credential and must be dropped; kept %v", keys(kept))
	}
	if len(drops) != 14 {
		t.Fatalf("want 14 drops, got %d: %+v", len(drops), drops)
	}
	// The reason is shown in the UI and the lock file: it must never echo the
	// credential it is refusing to copy.
	for _, d := range drops {
		if d.Reason == "" {
			t.Errorf("drop %q has no reason", d.Name)
		}
		if strings.Contains(d.Reason, "zz-literal") || strings.Contains(d.Reason, "ghp_") {
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
      "bool-flag":   {"command":"npx","args":["pkg","--no-auth"]}
    }}`)
	kept, drops := filterMCP(path, fakeProber{"npx": true})
	for _, want := range []string{"playwright", "fs", "remote", "empty-hdrs", "token-limit", "bool-flag"} {
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
