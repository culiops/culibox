package web

import (
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/mask"
	"github.com/culiops/agent-box/internal/session"
)

const (
	// activityDefaultLimit and activityMaxLimit bound one page of
	// GET /api/sessions/{id}/activity. A merge may run past the limit to finish
	// the millisecond it ends in (see mergeActivity).
	activityDefaultLimit = 100
	activityMaxLimit     = 200
	// activityAuditCap is the hard cap of each audit read (credentials,
	// approvals). Rows are not paged in SQL, so the cap is what bounds the work.
	activityAuditCap = 200
	// activityReasonMax is the rune budget of an approval reason in a row.
	activityReasonMax = 200
)

// ActivityAuditService reads a session's broker audit rows, newest first, for
// the activity timeline. Implemented by ActivityAuditAdapter; nil on the API =
// the credential broker is not running, and the timeline has transcript rows only.
type ActivityAuditService interface {
	ActivityCredentials(id string, limit int) ([]broker.Row, error)
	ActivityApprovals(id string, limit int) ([]broker.ApprovalRow, error)
}

// activityResponse is the wire shape of GET /api/sessions/{id}/activity.
type activityResponse struct {
	Claude       bool                    `json:"claude"`
	Events       []session.TimelineEvent `json:"events"`
	More         bool                    `json:"more"`
	Partial      bool                    `json:"partial"`
	Files        []session.FileChange    `json:"files"`
	Summary      activitySummary         `json:"summary"`
	WaitingSince int64                   `json:"waiting_since"`
}

type activitySummary struct {
	ToolCalls    int   `json:"tool_calls"`
	FilesChanged int   `json:"files_changed"`
	Credentials  int   `json:"credentials"`
	RunningS     int64 `json:"running_s"`
}

// handleActivity serves GET /api/sessions/{id}/activity[?before=<ms>&limit=<n>]:
// the session's transcript timeline merged with the broker's credential and
// approval audit rows, newest first. It is for the signed-in browser only (a
// bearer token gets 403) and sends descriptions, never a tool result, file
// content or a free-text audit error: of an audit row only the provider, entry,
// outcome, time (and, for an approval, action, minutes and reason) are read.
// Audit rows older than the session's own creation are dropped, so a recycled
// id can never show another session's history.
func (a *API) handleActivity(w http.ResponseWriter, r *http.Request, sid string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if credKindOf(r) != credCookie {
		writeErr(w, http.StatusForbidden, "activity is read from the web UI")
		return
	}
	limit := activityDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad limit")
			return
		}
		switch {
		case n <= 0:
			limit = activityDefaultLimit
		case n > activityMaxLimit:
			limit = activityMaxLimit
		default:
			limit = n
		}
	}
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "bad before")
			return
		}
		before = n
	}
	info, ok := a.Sessions.Get(sid)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	tl, ok := a.Sessions.TimelineFor(sid, before, limit)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}

	var cred, appr []session.TimelineEvent
	var credMore, apprMore bool
	minted := 0
	if a.Activity != nil {
		// Whole seconds: audit rows have no finer clock, so a row written in the
		// second the session was created is the session's own.
		since := info.Created.Truncate(time.Second)
		cred, credMore, minted = credentialEvents(a.Activity, sid, since, before)
		appr, apprMore = approvalEvents(a.Activity, sid, since, before)
	}
	events, more := mergeActivity(limit, tl.Events, cred, appr)
	files := tl.Files
	if files == nil {
		files = []session.FileChange{}
	}
	resp := activityResponse{
		Claude:  tl.Claude,
		Events:  events,
		More:    more || tl.More || credMore || apprMore,
		Partial: tl.Partial,
		Files:   files,
		Summary: activitySummary{
			ToolCalls: tl.ToolCalls, FilesChanged: len(files), Credentials: minted,
			RunningS: runningSeconds(info, events),
		},
		WaitingSince: tl.WaitingSince,
	}
	writeJSON(w, http.StatusOK, resp)
}

// runningSeconds is how long the session has run: to now while it is live, to
// its newest row once it has exited (the registry keeps no end time).
func runningSeconds(info session.Info, events []session.TimelineEvent) int64 {
	if info.Created.IsZero() {
		return 0
	}
	end := time.Now()
	if info.Exited {
		if len(events) == 0 {
			return 0
		}
		end = time.UnixMilli(events[0].TS)
	}
	if s := int64(end.Sub(info.Created) / time.Second); s > 0 {
		return s
	}
	return 0
}

// credentialEvents turns the session's credential audit rows into timeline
// events older than before (0 = all) and not older than since. more is true when
// the capped read may leave rows below the cursor; minted counts the successful
// rows since the session began (whatever the page).
func credentialEvents(src ActivityAuditService, sid string, since time.Time, before int64) (evs []session.TimelineEvent, more bool, minted int) {
	rows, err := src.ActivityCredentials(sid, activityAuditCap)
	if err != nil {
		log.Printf("activity %s: reading credential rows: %v", sid, err)
		return nil, false, 0
	}
	for _, r := range rows {
		if r.Time.Before(since) {
			continue
		}
		state, show := credentialState(r.Outcome)
		if !show {
			continue
		}
		if state == "minted" {
			minted++
		}
		ts := r.Time.UnixMilli()
		if before > 0 && ts >= before {
			continue
		}
		evs = append(evs, session.TimelineEvent{TS: ts, Kind: "credential", Detail: credentialDetail(r), State: state})
	}
	return evs, auditWindowOpen(len(rows), rows, func(i int) time.Time { return rows[i].Time }, since, before), minted
}

// credentialState maps a broker outcome to a row state. Base-role probes and
// GitHub revokes are the daemon's own housekeeping, not something the agent
// used, so they are not rows.
func credentialState(o broker.Outcome) (state string, show bool) {
	switch o {
	case broker.OutcomeMinted, broker.OutcomeFetched:
		return "minted", true
	case broker.OutcomeProbeMinted, broker.OutcomeProbeFailed, broker.OutcomeRevoked, broker.OutcomeRevokeFailed:
		return "", false
	}
	return "refused", true
}

// credentialDetail is "<provider> <entry>", e.g. "aws culipulse-prod". The
// entry is the named connection or token entry; a static token's is its Subject.
func credentialDetail(r broker.Row) string {
	provider := r.Provider
	if provider == "" {
		provider = broker.ProviderAWS
	}
	entry := r.Entry
	if entry == "" && (provider == broker.ProviderStatic || provider == broker.ProviderGitHub) {
		entry = r.Subject
	}
	return activityText(provider+" "+entry, 120)
}

// approvalEvents is credentialEvents for the write-window log. Sign-in rows are
// not part of a window's life and are not shown.
func approvalEvents(src ActivityAuditService, sid string, since time.Time, before int64) (evs []session.TimelineEvent, more bool) {
	rows, err := src.ActivityApprovals(sid, activityAuditCap)
	if err != nil {
		log.Printf("activity %s: reading approval rows: %v", sid, err)
		return nil, false
	}
	for _, r := range rows {
		if r.Time.Before(since) {
			continue
		}
		switch r.Action {
		case broker.ActionGranted, broker.ActionExtended, broker.ActionDropped,
			broker.ActionExpired, broker.ActionEnded, broker.ActionRefused:
		default:
			continue
		}
		ts := r.Time.UnixMilli()
		if before > 0 && ts >= before {
			continue
		}
		detail := string(r.Action)
		if r.Minutes > 0 {
			detail += " " + strconv.Itoa(r.Minutes) + " min"
		}
		if reason := activityText(r.Reason, activityReasonMax); reason != "" {
			detail += ` · "` + reason + `"`
		}
		evs = append(evs, session.TimelineEvent{TS: ts, Kind: "approval", Detail: detail, State: string(r.Action)})
	}
	return evs, auditWindowOpen(len(rows), rows, func(i int) time.Time { return rows[i].Time }, since, before)
}

// auditWindowOpen reports whether a capped, newest-first read may have left
// older rows that this cursor could still reach: the read hit its cap and its
// oldest row is still inside the session's life and below the cursor.
func auditWindowOpen[T any](n int, rows []T, at func(int) time.Time, since time.Time, before int64) bool {
	if n < activityAuditCap {
		return false
	}
	oldest := at(n - 1)
	return !oldest.Before(since) && (before == 0 || oldest.UnixMilli() < before)
}

// activityText makes audit text safe for one row: masked, control characters
// and runs of space reduced to single spaces, cut to max runes.
func activityText(s string, max int) string {
	s = mask.Display(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

// mergeActivity interleaves the transcript page with the audit events, newest
// first, and cuts the result to limit. Like TimelineFor, the cut finishes the
// millisecond it lands in, so a client paging with before=<oldest ts> never
// loses a same-ms row. more is set when the cut left anything behind.
func mergeActivity(limit int, lists ...[]session.TimelineEvent) ([]session.TimelineEvent, bool) {
	var all []session.TimelineEvent
	for _, l := range lists {
		all = append(all, l...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TS > all[j].TS })
	if all == nil {
		return []session.TimelineEvent{}, false
	}
	if len(all) <= limit {
		return all, false
	}
	cut := limit
	for cut < len(all) && all[cut].TS == all[cut-1].TS {
		cut++
	}
	return all[:cut], cut < len(all)
}
