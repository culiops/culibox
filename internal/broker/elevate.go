package broker

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/awsscope"
)

// The credential kind recorded on an approval row. Only these labels are ever
// stored, never caller-supplied text.
const (
	CredCookie = "cookie"
	CredBearer = "bearer"
	credNone   = "none"

	minElevatedRemaining = credTTL + time.Minute // 16 min: STS minimum + clock skew (spec §5.2)
	maxReasonLen         = 200                   // characters
	maxReasonBytes       = 4 * maxReasonLen      // a valid reason is at most 4 bytes per character
)

// ErrElevateRefused is wrapped by every refusal; its text after ": " is a plain
// message the web layer may show.
var ErrElevateRefused = errors.New("elevation refused")

// ElevateRequest is a human's request for a write window. Cred is the kind of
// credential the request arrived with; only a signed-in browser (CredCookie)
// may grant.
type ElevateRequest struct {
	Minutes                    int
	Reason, ConfirmLabel, Cred string
}

// ElevationView is the timer-free, exported state of a session's write window.
type ElevationView struct {
	Active    bool      `json:"active"`
	Until     time.Time `json:"until,omitzero"`
	GrantedAt time.Time `json:"granted_at,omitzero"`
	Reason    string    `json:"reason,omitempty"`
	LastValid time.Time `json:"last_valid,omitzero"`
}

// stopper is the part of *time.Timer the overlay uses; tests inject fakes.
type stopper interface{ Stop() bool }

// elevation is one session's write window. In memory only (D5): a daemon
// restart drops it, and credentials already handed out still end by until.
type elevation struct {
	until, grantedAt time.Time
	lastExp          time.Time // latest Expiration of an elevated credential handed out
	reason           string
	conn             string // the AWS connection the window is for; "" = the legacy binding
	gen              uint64 // bumped on every grant/extend; a stale timer or mint compares it
	pending          bool   // granted but its approval row is not written yet: grants nothing, reported as no window
	timer            stopper
	row              ApprovalRow // identifiers of the grant, for the rows written later (no Time/Action)
}

// refusedKey scopes the refusal throttle.
type refusedKey struct{ sid, cred string }

// SetClaimCheck installs the grant-time check that no other box uses the
// session's workspace (spec correction 8). Set before the broker serves.
func (b *Broker) SetClaimCheck(f func(sessionID string) error) { b.claimCheck = f }

// credLabel maps a caller's credential kind onto the fixed labels; anything
// else is "none".
func credLabel(c string) string {
	if c == CredCookie || c == CredBearer {
		return c
	}
	return credNone
}

// identify fills a row's profile, role and account from the session: the
// elevate role when the binding has one, since that is what a grant is about.
func identify(row *ApprovalRow, sv SessionView) {
	row.Profile = sv.Agent
	if sv.AWS == nil {
		return
	}
	row.RoleARN = sv.AWS.RoleARN
	if sv.AWS.Elevate != nil {
		row.RoleARN = sv.AWS.Elevate.RoleARN
	}
	row.AccountID = awsscope.AccountOf(row.RoleARN)
}

func validReason(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxReasonLen {
		return false
	}
	for _, r := range s {
		// Control characters, line/paragraph separators (U+2028/9) and format
		// characters (bidi overrides, zero-width joiners) are not "one line".
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Cf) {
			return false
		}
	}
	return true
}

// Elevate grants (or extends) a write window for a session, or refuses with an
// error wrapping ErrElevateRefused. Every refusal and every grant is audited; a
// grant whose row cannot be written does not stand.
func (b *Broker) Elevate(sid string, req ElevateRequest) (ElevationView, error) {
	now := b.now()
	sv, found := b.lookup(sid)
	row := ApprovalRow{Time: now, SessionID: sid, Minutes: req.Minutes, Reason: req.Reason, Cred: credLabel(req.Cred)}
	if found {
		identify(&row, sv)
	}
	refuse := func(msg string) (ElevationView, error) {
		b.recordRefusal(row, msg)
		return ElevationView{}, fmt.Errorf("%w: %s", ErrElevateRefused, msg)
	}
	switch {
	case b.appr == nil:
		return refuse("the approval audit is not available, so nothing can be granted")
	case req.Cred != CredCookie:
		return refuse("granting write access needs a signed-in browser session")
	case !found || !sv.Live:
		return refuse("this session is not running")
	case !sv.Boxed || sv.AWS == nil:
		return refuse("this session has no AWS binding in a box")
	case sv.AWS.Elevate == nil:
		return refuse("this session's profile did not allow elevation when it started; add elevate to the profile and start a new session")
	case !sv.Exclusive:
		return refuse("this session's box is shared; restart it to get its own box")
	}
	maxMin := sv.AWS.Elevate.Max()
	if (req.Minutes != 30 && req.Minutes != 60) || req.Minutes > maxMin {
		return refuse(fmt.Sprintf("the window must be 30 or 60 minutes, at most %d", maxMin))
	}
	if !validReason(req.Reason) {
		return refuse("the reason must be one line of at most 200 characters")
	}
	if sv.AWS.Prod && req.ConfirmLabel != sv.AWS.Label {
		return refuse("type the binding label to grant write access on a production account")
	}
	if b.claimCheck != nil { // outside mu: it may take other locks
		if err := b.claimCheck(sid); err != nil {
			return refuse(err.Error())
		}
	}

	// The claim check can take a while: the window is measured from here.
	now = b.now()
	row.Time = now
	row.Reason = normalizeReason(req.Reason) // what is stored is what is shown
	until := now.Add(time.Duration(req.Minutes) * time.Minute)
	action := ActionGranted

	b.mu.Lock()
	old := b.elev[sid]
	if old != nil && old.pending {
		b.mu.Unlock()
		return refuse("a grant for this session is still being recorded; try again in a moment")
	}
	var gen uint64
	if old == nil {
		// A new window starts pending: it grants nothing (elevatedLocked) until its
		// approval row is written. An extend leaves the old window as it is, in force
		// and unchanged, until its own row is written.
		b.gen++
		gen = b.gen
		e := &elevation{until: until, grantedAt: now, reason: row.Reason, row: row, gen: gen, pending: true}
		e.timer = b.after(until.Sub(now), func() { b.expire(sid, gen) })
		b.elev[sid] = e
		delete(b.dropped, sid)
	} else {
		action, gen = ActionExtended, old.gen
	}
	b.mu.Unlock()

	row.Action, row.Until = action, until
	if err := b.appr.RecordApproval(row); err != nil {
		b.mu.Lock()
		if cur := b.elev[sid]; cur != nil && cur.gen == gen { // fail closed, extend included
			b.endLocked(sid, cur, now)
		}
		b.mu.Unlock()
		log.Printf("broker: approval audit write failed (%v); grant for %s withdrawn", err, sid)
		return ElevationView{}, fmt.Errorf("%w: the grant could not be audited, so it was not made", ErrElevateRefused)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.elev[sid]
	if cur == nil || cur.gen != gen { // dropped, expired or forgotten while the row was written
		log.Printf("broker: window for %s ended before its %s row took effect", sid, action)
		return ElevationView{}, fmt.Errorf("%w: the write window ended before the grant took effect", ErrElevateRefused)
	}
	if action == ActionGranted {
		cur.pending = false
		return viewOf(cur), nil
	}
	cur.timer.Stop() // the extend takes effect now: new until, new gen, new timer
	b.gen++
	ng := b.gen
	ne := &elevation{until: until, grantedAt: cur.grantedAt, lastExp: cur.lastExp, reason: row.Reason, row: row, gen: ng}
	ne.timer = b.after(until.Sub(b.now()), func() { b.expire(sid, ng) }) // the write took time: measure what is left now
	b.elev[sid] = ne
	return viewOf(ne), nil
}

// elevatedLocked is the one predicate the mint path uses to decide whether a
// credential may be write-capable (spec §5.2): a window exists, its approval row
// has been written (not pending), and at least minElevatedRemaining is left.
// Caller holds b.mu.
func (b *Broker) elevatedLocked(sid string, now time.Time) (*elevation, bool) {
	e := b.elev[sid]
	if e == nil || e.pending || e.until.Sub(now) < minElevatedRemaining {
		return nil, false
	}
	return e, true
}

// normalizeReason collapses whitespace to single spaces, as the audit store does,
// so the reason shown is the reason recorded.
func normalizeReason(s string) string { return strings.Join(strings.Fields(s), " ") }

func viewOf(e *elevation) ElevationView {
	return ElevationView{Active: true, Until: e.until, GrantedAt: e.grantedAt, Reason: e.reason}
}

// endLocked removes a session's overlay (caller holds b.mu), stops its timer and
// remembers how long already-issued write credentials stay valid.
func (b *Broker) endLocked(sid string, e *elevation, now time.Time) {
	e.timer.Stop()
	delete(b.elev, sid)
	if e.lastExp.After(now) {
		b.dropped[sid] = e.lastExp
	}
}

// noteElevatedExp raises the recorded expiry of the latest write credential
// handed out. The mint path calls it under b.mu (caller holds b.mu).
func (b *Broker) noteElevatedExp(sid string, exp time.Time) {
	if e := b.elev[sid]; e != nil && exp.After(e.lastExp) {
		e.lastExp = exp
	}
}

// expire ends the window when its timer fires. gen guards against a timer that
// an extend or drop has already replaced.
func (b *Broker) expire(sid string, gen uint64) {
	b.mu.Lock()
	e := b.elev[sid]
	if e == nil || e.gen != gen {
		b.mu.Unlock()
		return
	}
	delete(b.elev, sid)
	row := e.row
	b.mu.Unlock()
	row.Time, row.Action, row.Until, row.Cred, row.Reason = b.now(), ActionExpired, e.until, "", e.reason
	b.recordApproval(row)
}

// Drop ends a session's write window at once. cred is the kind of credential the
// request arrived with. ok is false when there was no window. The view carries
// LastValid: how long write credentials already issued still work.
func (b *Broker) Drop(sid, cred string) (ElevationView, bool) {
	now := b.now()
	b.mu.Lock()
	e := b.elev[sid]
	if e == nil {
		b.mu.Unlock()
		return ElevationView{}, false
	}
	b.endLocked(sid, e, now)
	view := ElevationView{LastValid: b.dropped[sid]}
	b.mu.Unlock()
	row := e.row
	row.Time, row.Action, row.Until, row.Cred, row.Reason = now, ActionDropped, e.until, credLabel(cred), e.reason
	b.recordApproval(row)
	return view, true
}

// Elevation reports a session's window: Active while one is in force. A grant
// whose approval row is still being written (pending) is not reported: it is not
// in force. After a drop, it reports LastValid (and ok) until the last write
// credential issued has expired.
func (b *Broker) Elevation(sid string) (ElevationView, bool) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.elev[sid]; e != nil {
		if e.pending {
			return ElevationView{}, false
		}
		return viewOf(e), true
	}
	if lv, ok := b.dropped[sid]; ok {
		if lv.After(now) {
			return ElevationView{LastValid: lv}, true
		}
		delete(b.dropped, sid)
	}
	return ElevationView{}, false
}

// RefuseElevation audits a refusal the web layer decided itself (a bearer
// credential, a bad origin). Only a session the daemon knows is recorded: the
// throttle is per (session, cred), so a caller rotating made-up ids would
// otherwise write an unbounded number of rows.
func (b *Broker) RefuseElevation(sid, cred, reason string) {
	sv, ok := b.lookup(sid)
	if !ok {
		return
	}
	row := ApprovalRow{Time: b.now(), SessionID: sid, Cred: credLabel(cred)}
	identify(&row, sv)
	b.recordRefusal(row, reason)
}

// recordRefusal writes at most one refused row per (session, cred) per second,
// so a looping client cannot grow the audit store. The throttle map is pruned
// once a second.
func (b *Broker) recordRefusal(row ApprovalRow, msg string) {
	if b.appr == nil {
		return
	}
	key := refusedKey{row.SessionID, row.Cred}
	b.mu.Lock()
	now := b.now()
	if now.Sub(b.lastRefuse) >= deniedWindow {
		b.lastRefuse = now
		for k, t := range b.refused {
			if now.Sub(t) >= deniedWindow {
				delete(b.refused, k)
			}
		}
	}
	if t, ok := b.refused[key]; ok && now.Sub(t) < deniedWindow {
		b.mu.Unlock()
		return
	}
	b.refused[key] = now
	b.mu.Unlock()
	row.Action, row.Error = ActionRefused, msg
	b.recordApproval(row)
}

// recordApproval writes a row whose failure cannot change an outcome already
// taken (expiry, drop, refusal), so it is only logged.
func (b *Broker) recordApproval(r ApprovalRow) {
	if b.appr == nil {
		return
	}
	if err := b.appr.RecordApproval(r); err != nil {
		log.Printf("broker: approval audit write failed (%v)", err)
	}
}
