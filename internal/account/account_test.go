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
