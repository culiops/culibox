package main

import (
	"context"
	"fmt"
	"time"

	"github.com/culiops/agent-box/internal/bugreport"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/doctor"
	"github.com/culiops/agent-box/internal/session"
)

// bugReportTimeout bounds the doctor run behind Settings -> About -> Report a
// problem: the request must answer even when a check hangs.
const bugReportTimeout = 30 * time.Second

// bugDeps is everything the daemon's bug-report closure reads. base carries the
// fixed part of the bundle (producer, commit, home, host, config); the rest is
// read fresh on every request.
type bugDeps struct {
	checks   func() []doctor.Result
	timeout  time.Duration
	start    time.Time
	base     bugreport.Input
	profiles func() []config.AgentConfig
	sessions func() []session.Info
	logs     func() []string
}

// buildBugReport returns the closure behind GET /api/bugreport. Doctor runs in
// its own goroutine so a hung check cannot hold the request past d.timeout; the
// goroutine may outlive it (each doctor check has its own timeout), and its
// buffered result channel means it never blocks once abandoned. A panic inside
// a check becomes a note, not a daemon crash.
func buildBugReport(d bugDeps) func(context.Context) (string, string) {
	return func(ctx context.Context) (string, string) {
		type outcome struct {
			res []doctor.Result
			err any
		}
		ch := make(chan outcome, 1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					ch <- outcome{err: p}
				}
			}()
			ch <- outcome{res: d.checks()}
		}()
		in := d.base
		in.Now = time.Now().UTC()
		timer := time.NewTimer(d.timeout)
		defer timer.Stop()
		select {
		case o := <-ch:
			if o.err != nil {
				in.DoctorNote = "doctor failed; run agent-box bug-report in a terminal"
			} else {
				in.Doctor = o.res
			}
		case <-timer.C:
			in.DoctorNote = fmt.Sprintf("doctor timed out after %s; run agent-box bug-report in a terminal", d.timeout)
		case <-ctx.Done():
			in.DoctorNote = "doctor was cancelled; run agent-box bug-report in a terminal"
		}
		in.Profiles = d.profiles()
		in.Live = &bugreport.Live{
			Uptime:   time.Since(d.start),
			Sessions: bugSessionCounts(d.sessions()),
			Logs:     d.logs(),
		}
		return bugreport.Markdown(in), bugreport.IssueURL(in)
	}
}

// bugSessionCounts groups sessions by "<runtime>/<status>"; an empty runtime is
// the host tier and an exited session counts as "exited" whatever its last
// status was.
func bugSessionCounts(list []session.Info) map[string]int {
	out := map[string]int{}
	for _, s := range list {
		rt, st := s.Runtime, s.Status
		if rt == "" {
			rt = "host"
		}
		if s.Exited {
			st = "exited"
		}
		if st == "" {
			st = "unknown"
		}
		out[rt+"/"+st]++
	}
	return out
}
