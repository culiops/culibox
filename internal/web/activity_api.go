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

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/mask"
	"github.com/culiops/culibox/internal/session"
)

const (
	// activityDefaultLimit and activityMaxLimit bound one page of
	// GET /api/sessions/{id}/activity. A merge may run past the limit to finish
	// the millisecond it ends in (see mergeActivity).
	activityDefaultLimit = 100
	activityMaxLimit     = 200
	// activityAuditCap is the hard cap of each audit read (credentials,
	// approvals). The reads page in SQL (ts window plus LIMIT), so one page is
	// at most this many rows however long the session's audit history is.
	activityAuditCap = 200
	// activityReasonMax is the rune budget of an approval reason in a row.
	activityReasonMax = 200
)

// ActivityAuditService reads a session's broker audit rows, newest first, for
// the activity timeline. Implemented by ActivityAuditAdapter; nil on the API =
// the credential broker is not running, and the timeline has transcript rows only.
type ActivityAuditService interface {
	// Times are unix seconds: rows with ts >= sinceTS and, when beforeTS > 0,
	// ts < beforeTS, at most limit of them.
	ActivityCredentials(id string, sinceTS, beforeTS int64, limit int) ([]broker.Row, error)
	ActivityApprovals(id string, sinceTS, beforeTS int64, limit int) ([]broker.ApprovalRow, error)
	// ActivityMinted counts every credential the session was handed since sinceTS.
	ActivityMinted(id string, sinceTS int64) (int, error)
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
	var credMore, apprMore, auditFailed bool
	minted := 0
	if a.Activity != nil {
		// Whole seconds: audit rows have no finer clock, so a row written in the
		// second the session was created is the session's own.
		since := info.Created.Truncate(time.Second).Unix()
		// before is in ms; an audit row at second s shows at s*1000.
		var beforeTS int64
		if before > 0 {
			beforeTS = (before + 999) / 1000
		}
		var err error
		if cred, credMore, err = credentialEvents(a.Activity, sid, since, beforeTS); err != nil {
			log.Printf("activity %s: reading credential rows: %v", sid, err)
			auditFailed = true
		}
		if appr, apprMore, err = approvalEvents(a.Activity, sid, since, beforeTS); err != nil {
			log.Printf("activity %s: reading approval rows: %v", sid, err)
			auditFailed = true
		}
		if minted, err = a.Activity.ActivityMinted(sid, since); err != nil {
			log.Printf("activity %s: counting credentials: %v", sid, err)
			auditFailed = true
		}
	}
	events, more := mergeActivity(limit,
		activitySource{tl.Events, tl.More}, activitySource{cred, credMore}, activitySource{appr, apprMore})
	files := tl.Files
	if files == nil {
		files = []session.FileChange{}
	}
	resp := activityResponse{
		Claude: tl.Claude,
		Events: events,
		More:   more,
		// A failed audit read leaves the timeline and the credential count
		// incomplete; say so rather than claim an empty history.
		Partial: tl.Partial || auditFailed,
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

// credentialEvents turns one page of the session's credential audit rows
// (read in SQL: not older than since, older than beforeTS, newest first, capped)
// into timeline events. more is true when the read may have left older rows.
func credentialEvents(src ActivityAuditService, sid string, since, beforeTS int64) ([]session.TimelineEvent, bool, error) {
	rows, err := src.ActivityCredentials(sid, since, beforeTS, activityAuditCap)
	if err != nil {
		return nil, false, err
	}
	rows, more := completeSeconds(rows, func(r broker.Row) int64 { return r.Time.Unix() })
	var evs []session.TimelineEvent
	for _, r := range rows {
		state, show := credentialState(r.Outcome)
		// The query already filters; these keep a misbehaving source honest.
		if !show || r.Time.Unix() < since || (beforeTS > 0 && r.Time.Unix() >= beforeTS) {
			continue
		}
		evs = append(evs, session.TimelineEvent{TS: r.Time.UnixMilli(), Kind: "credential", Detail: credentialDetail(r), State: state, Provider: broker.NormalizeProvider(r.Provider)})
	}
	return evs, more, nil
}

// credentialState maps a broker outcome to a row state. Base-role probes and
// GitHub revokes are the daemon's own housekeeping, not something the agent
// used, so they are not rows. A denied_* outcome is a containment refusal (the
// gate said no, which is it working: "refused"); every other outcome that did
// not mint is something that broke ("failed"), and the view shows the two
// differently.
func credentialState(o broker.Outcome) (state string, show bool) {
	switch o {
	case broker.OutcomeMinted, broker.OutcomeFetched:
		return "minted", true
	case broker.OutcomeProbeMinted, broker.OutcomeProbeFailed, broker.OutcomeRevoked, broker.OutcomeRevokeFailed:
		return "", false
	}
	if strings.HasPrefix(string(o), "denied_") {
		return "refused", true
	}
	return "failed", true
}

// credentialDetail is "<provider> <entry>", e.g. "aws culipulse-prod". The
// entry is the named connection or token entry; a static token's is its Subject.
func credentialDetail(r broker.Row) string {
	provider := broker.NormalizeProvider(r.Provider)
	entry := r.Entry
	if entry == "" && (provider == broker.ProviderStatic || provider == broker.ProviderGitHub) {
		entry = r.Subject
	}
	return activityText(provider+" "+entry, 120)
}

// approvalEvents is credentialEvents for the write-window log.
func approvalEvents(src ActivityAuditService, sid string, since, beforeTS int64) ([]session.TimelineEvent, bool, error) {
	rows, err := src.ActivityApprovals(sid, since, beforeTS, activityAuditCap)
	if err != nil {
		return nil, false, err
	}
	rows, more := completeSeconds(rows, func(r broker.ApprovalRow) int64 { return r.Time.Unix() })
	var evs []session.TimelineEvent
	for _, r := range rows {
		switch r.Action {
		case broker.ActionGranted, broker.ActionExtended, broker.ActionDropped,
			broker.ActionExpired, broker.ActionEnded, broker.ActionRefused:
		default:
			continue
		}
		if r.Time.Unix() < since || (beforeTS > 0 && r.Time.Unix() >= beforeTS) {
			continue
		}
		detail := string(r.Action)
		if r.Minutes > 0 {
			detail += " " + strconv.Itoa(r.Minutes) + " min"
		}
		if reason := activityText(r.Reason, activityReasonMax); reason != "" {
			detail += ` · "` + reason + `"`
		}
		evs = append(evs, session.TimelineEvent{TS: r.Time.UnixMilli(), Kind: "approval", Detail: detail, State: string(r.Action)})
	}
	return evs, more, nil
}

// completeSeconds handles the cap of a newest-first audit read. A read that hit
// its cap may have stopped inside a second, and the next page asks for strictly
// older seconds, so the trailing partial second is dropped (it is read whole by
// that next page) and more is true. A capped read of a single second is kept
// as is.
func completeSeconds[T any](rows []T, at func(T) int64) ([]T, bool) {
	if len(rows) < activityAuditCap {
		return rows, false
	}
	last := at(rows[len(rows)-1])
	n := len(rows)
	for n > 0 && at(rows[n-1]) == last {
		n--
	}
	if n == 0 {
		return rows, true
	}
	return rows[:n], true
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

// activitySource is one newest-first list of events and whether older ones
// exist beyond its end.
type activitySource struct {
	evs  []session.TimelineEvent
	more bool
}

// mergeActivity interleaves the sources, newest first, and cuts the result to
// limit. Two things keep a client paging with before=<oldest ts> from losing
// rows. A source that has more behind it is only known down to its oldest row,
// so nothing older than the highest such floor is returned (the next page
// reads it). And, like TimelineFor, the limit cut finishes the millisecond it
// lands in. more is set when anything is left behind.
func mergeActivity(limit int, sources ...activitySource) ([]session.TimelineEvent, bool) {
	var all []session.TimelineEvent
	more := false
	var floor int64
	for _, src := range sources {
		all = append(all, src.evs...)
		if src.more {
			more = true
			if n := len(src.evs); n > 0 && src.evs[n-1].TS > floor {
				floor = src.evs[n-1].TS
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TS > all[j].TS })
	if floor > 0 {
		n := len(all)
		for n > 0 && all[n-1].TS < floor {
			n--
		}
		all = all[:n]
	}
	if all == nil {
		return []session.TimelineEvent{}, more
	}
	if len(all) <= limit {
		return all, more
	}
	cut := limit
	for cut < len(all) && all[cut].TS == all[cut-1].TS {
		cut++
	}
	return all[:cut], more || cut < len(all)
}
