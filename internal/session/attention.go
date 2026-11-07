package session

// attentionOf says how a listed session asks for the operator, in one word the
// desktop sidebar can group by: "block" (an agent is blocked on you), "done"
// (it finished its turn) or "" (nothing to look at).
//
// It reads only Exited, Activity and Need, which List has already settled, and
// makes no agent check: a codex or shell session whose Need is "sso" or
// "store_locked", or whose activity is "waiting", is judged the same way as a
// claude one. An exited session is always "": applyAWSState can still leave
// Need "sso" on one, and a dead session blocks on nobody.
//
// A "waiting" session with no Need (its transcript cannot be read, or the agent
// has none) is "block", not "". Calling it "done" would claim a finished turn we
// never saw, and "" would hide a session that may well be blocked; the cost of
// being wrong is one extra amber row rather than a hidden prompt (inferred, not
// measured).
//
// Need "ask", "sso" and "store_locked" all mean something is waiting on the
// operator, so they are "block"; Need "turn" is "done". A Need this function
// does not know falls through to the Activity rule, so a future need is never
// silently dropped.
func attentionOf(in Info) string {
	if in.Exited {
		return ""
	}
	switch in.Need {
	case "ask", "sso", "store_locked":
		return "block"
	case "turn":
		return "done"
	}
	if in.Activity == "waiting" {
		return "block"
	}
	return ""
}
