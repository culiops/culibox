package main

import (
	"fmt"
	"io"
	"time"

	"github.com/culiops/culibox/internal/bugreport"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/doctor"
	"github.com/culiops/culibox/internal/store"
)

// runBugReport writes the redacted bundle to out and the issue link plus a
// paste hint to errw. It exits 0 whatever doctor found: the report is the
// product, and a FAIL in it is the very thing being reported. host and home are
// both passed on because the redaction depends on them.
func runBugReport(in doctor.Input, host, goos string, out, errw io.Writer) int {
	profiles := append([]config.AgentConfig(nil), in.Config.Agents...)
	note := ""
	// A broken config or unusable home is already a doctor FAIL; there is no
	// data dir to read profiles from then.
	if in.Home != "" && in.ConfigErr == nil {
		dataDir := in.Config.ResolvedDataDir(in.Home)
		st, err := store.Open(in.Config.ResolvedProfilesPath(dataDir))
		if err != nil {
			note = "stored profiles could not be read; the profile list shows the config seeds only"
		} else {
			profiles = append(profiles, st.List()...)
		}
	}
	bi := bugreport.Input{
		Producer:     "cli",
		Version:      version,
		Commit:       in.Commit,
		Now:          time.Now().UTC(),
		Home:         in.Home,
		HomeResolved: doctor.ResolveHome(in.Home),
		Host:         host,
		Doctor:       doctor.Checks(in),
		DoctorNote:   note,
		Config:       in.Config,
		Profiles:     profiles,
		LogHint:      bugReportLogHint(goos),
	}
	fmt.Fprint(out, bugreport.Markdown(bi))
	fmt.Fprintf(errw, "Issue link: %s\n", bugreport.IssueURL(bi))
	fmt.Fprintln(errw, "macOS: culibox bug-report | pbcopy")
	return 0
}

// bugReportLogHint says where this platform's daemon logs are.
func bugReportLogHint(goos string) string {
	if goos == "darwin" {
		return "the terminal that started culibox serve (stderr)"
	}
	return "journalctl --user -u culibox.service (or culibox-prod.service) --since -1h"
}
