package web

// Refusal reasons the fs API and the box file endpoint send in the JSON
// "error" field. The viewer and the tree toasts show them verbatim, so each
// says WHY in words a user can act on. Founder-approved copy (#50); change it
// there first.
const (
	msgOutside  = "outside your home folder and the Claude scratch folder — not viewable"
	msgDenied   = "agent-box's own data — not viewable"
	msgEscape   = "blocked: a link here leads out of the box's workspace"
	msgPerm     = "no permission to read this"
	msgNotReg   = "not a regular file (pipe, socket or device) — not opened"
	msgReadOnly = "the Claude scratch folder is read-only"
	msgBoxOnly  = "inside the box only — just /tmp/playwright-mcp is shared out"
	// msgBlocked is an open through a host root that failed for a reason we
	// did not identify; it never claims one.
	msgBlocked = "blocked"
)
