package account

import "testing"

func TestValidName(t *testing.T) {
	good := []string{"work", "a", "acct-2", "0x", "abcdefghijklmnopqrstuvwxyz012345"}
	bad := []string{"", "default", "Work", "-x", "a/b", "..", "a.b", "a b", "abcdefghijklmnopqrstuvwxyz0123456"}
	for _, n := range good {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if ValidName(n) == nil {
			t.Errorf("ValidName(%q) = nil, want error", n)
		}
	}
}

func TestAccountValidate(t *testing.T) {
	if err := (Account{Name: "w", Provider: "claude"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if (Account{Name: "w", Provider: "gemini"}).Validate() == nil {
		t.Fatal("unknown provider accepted")
	}
	if (Account{Name: "w", Provider: "claude", TokenSecret: "BAD NAME"}).Validate() == nil {
		t.Fatal("bad secret name accepted")
	}
	if err := (Account{Name: "w", Provider: "codex", TokenSecret: "CODEX_TOK"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIsAuthEnv(t *testing.T) {
	for _, n := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENAI_API_KEY"} {
		if !IsAuthEnv(n) {
			t.Errorf("%s should be auth env", n)
		}
	}
	if IsAuthEnv("ANTHROPIC_MODEL") {
		t.Error("ANTHROPIC_MODEL is not auth")
	}
}

// Measured from the claude 2.1.306 binary (2026-10-21): each of these routes
// inference to another provider, points it at another endpoint, or carries a
// credential. New ones keep appearing, so the rule goes by shape, not list.
func TestIsAuthEnvCoversProviderRouting(t *testing.T) {
	for _, n := range []string{
		"AWS_BEARER_TOKEN_BEDROCK", "AWS_BEARER_TOKEN_SOMETHING_NEW",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
		"CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_GATEWAY", "CLAUDE_CODE_USE_ANTHROPIC_AWS",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH", "CLAUDE_CODE_SKIP_VERTEX_AUTH",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_BEDROCK_BASE_URL",
		"ANTHROPIC_VERTEX_PROJECT_ID", "ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_RESOURCE",
		"ANTHROPIC_AWS_API_KEY", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_GOOGLE_CLOUD_PROJECT",
		"CLAUDE_CODE_API_BASE_URL", "CLAUDE_CODE_GATEWAY_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN",
		"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
		"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE",
		"CLAUDE_CODE_CUSTOM_OAUTH_URL", "CLAUDE_CODE_HOST_AUTH_ENV_VAR", "CLAUDE_CODE_SESSION_ACCESS_TOKEN",
		"CLAUDE_CODE_GATEWAY_HINT_HEADERS",
		// The binary's own auth-export table (pre-merge review, 2026-10-21).
		"CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_TRUSTED_DEVICE_TOKEN", "CLAUDE_SESSION_INGRESS_TOKEN_FILE",
		"CLAUDE_LOCAL_OAUTH_API_BASE", "CLAUDE_LOCAL_OAUTH_APPS_BASE", "CLAUDE_LOCAL_OAUTH_CONSOLE_BASE",
		"USE_LOCAL_OAUTH", "USE_STAGING_OAUTH", "AGENT_PROXY_AUTH_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN",
		"CLAUDE_CODE_FEDERATION_CACHE_DIR", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_ORGANIZATION_UUID",
		"CLAUDE_CODE_USER_EMAIL", "CLAUDE_CODE_ACCOUNT_TAGGED_ID", "CLAUDE_CODE_SUBSCRIPTION_TYPE",
		"CLAUDE_CODE_RATE_LIMIT_TIER", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CLAUDE_BRIDGE_BASE_URL",
		"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_BG_SOCKET_TOKENS_PATH",
	} {
		if !IsAuthEnv(n) {
			t.Errorf("%s should be auth env", n)
		}
	}
	for _, n := range []string{
		"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_MAX_OUTPUT_TOKENS",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "CLAUDE_CODE_SKIP_PROMPT_HISTORY",
		"AWS_PROFILE", "AWS_REGION", "ANTHROPIC", "CLAUDE_CODE_", "CLAUDE_", "CLAUDECODE", "CLAUDE_PID",
		// Model metadata describes a model and cannot route anything.
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME", "ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES",
	} {
		if IsAuthEnv(n) {
			t.Errorf("%s is not auth env", n)
		}
	}
}
