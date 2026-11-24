package session

// The capabilities of the agent binary a session starts, as named questions.
// Callers ask these, never "is it Claude" (CLAUDE.md, "One agent now, more
// later"): when another provider is scheduled, each answer changes here and
// nowhere else. Today the only agent with either is Claude Code.

// appendsSystemPrompt reports whether the agent takes extra system-prompt text
// as flags (--append-system-prompt <text>), the way the credential note and the
// clone line reach it.
func appendsSystemPrompt(bin string) bool { return isClaudeBin(bin) }

// takesMCPConfig reports whether the agent reads an MCP server config from a
// flag (--mcp-config <file>), the way a token entry's server reaches it.
func takesMCPConfig(bin string) bool { return isClaudeBin(bin) }
