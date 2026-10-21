package doctor

import "strings"

// checkCredFile fails when the web-defined credentials file did not load
// cleanly: it is unreadable or not valid JSON, or entries in it were skipped.
// The daemon starts anyway (so the screen that repairs it stays reachable),
// which is why this is a FAIL here: nothing else tells the operator that an
// entry a profile relies on is missing.
func checkCredFile(in Input) Result {
	if len(in.CredErrors) == 0 {
		return Result{Pass, "credentials file", "no problems"}
	}
	return Result{Fail, "credentials file", strings.Join(in.CredErrors, "; ")}
}
