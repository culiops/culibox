package broker

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/config"
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
	// dropHoldSkew extends WriteHold's hold on a dropped window past the STS
	// Expiration it recorded: that time is AWS's, compared with the local
	// clock (#325; the restart hold has the same minute in minElevatedRemaining).
	dropHoldSkew = time.Minute
)

// ErrElevateRefused is wrapped by every refusal; its text after ": " is a plain
// message the web layer may show.
var ErrElevateRefused = errors.New("elevation refused")

// ElevateRequest is a human's request for a write window. Cred is the kind of
// credential the request arrived with; only a signed-in browser (CredCookie)
// may grant. Name is the AWS connection the window is for: required for a
// session bound by connections, empty for the legacy binding.
type ElevateRequest struct {
	Minutes                          int
	Name, Reason, ConfirmLabel, Cred string
}

// ElevationView is the timer-free, exported state of a session's write window.
type ElevationView struct {
	Active    bool      `json:"active"`
	Until     time.Time `json:"until,omitzero"`
	GrantedAt time.Time `json:"granted_at,omitzero"`
	Reason    string    `json:"reason,omitempty"`
	LastValid time.Time `json:"last_valid,omitzero"`
	// Name is the AWS connection the window (or, after a drop, the write
	// credentials still valid) is for; empty for the legacy binding.
	Name string `json:"name,omitempty"`
}

// droppedWin is what a drop leaves behind: the connection whose write
// credentials were already issued, and the latest Expiration among them.
type droppedWin struct {
	conn      string
	lastValid time.Time
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
// conn "" is the legacy binding; a named connection the session has also sets
// the row's Entry, and one it lacks leaves the row with the profile alone.
func identify(row *ApprovalRow, sv SessionView, conn string) {
	row.Profile = sv.Agent
	bnd := sv.AWS
	if conn != "" {
		c, ok := config.AWSConnByName(sv.AWSConns, conn)
		if !ok {
			return
		}
		bnd, row.Entry = &c.AWSBinding, c.Name
	}
	if bnd == nil {
		return
	}
	row.RoleARN = bnd.RoleARN
	if bnd.Elevate != nil {
		row.RoleARN = bnd.Elevate.RoleARN
	}
	row.AccountID = awsscope.AccountOf(row.RoleARN)
}

// elevBinding resolves the binding a grant is for (spec §7.5). The legacy
// binding takes no name; a session bound by connections must name one of its
// own. entry is what a refused row records for the name: the name when it is
// well formed, invalidEntry when it is not, "" when none was given.
func elevBinding(sv SessionView, name string) (bnd *config.AWSBinding, entry, why string) {
	switch {
	case len(sv.AWSConns) > 0 && name == "":
		return nil, "", "name the connection to elevate"
	case len(sv.AWSConns) > 0:
		if !config.ValidTokenName(name) {
			return nil, invalidEntry, "that is not a valid connection name"
		}
		c, ok := config.AWSConnByName(sv.AWSConns, name)
		if !ok {
			return nil, name, fmt.Sprintf("this session has no AWS connection named %q", name)
		}
		return &c.AWSBinding, name, ""
	case sv.AWS == nil:
		return nil, "", "this session has no AWS binding in a box"
	case name != "":
		entry = name
		if !config.ValidTokenName(name) {
			entry = invalidEntry
		}
		return nil, entry, "this session has no named AWS connections; grant without a name"
	}
	return sv.AWS, "", ""
}

// grantConflictLocked is why a grant for conn cannot be made now on top of the
// binding's own rules: the session has one write window at a time, so another
// connection's live window, or the write credentials it issued before a drop
// that are still valid, block it. "" = no conflict. Caller holds b.mu.
func (b *Broker) grantConflictLocked(sid, conn string, now time.Time) string {
	if e := b.elev[sid]; e != nil && e.conn != conn {
		return fmt.Sprintf("drop %s first", e.conn)
	}
	if d, ok := b.dropped[sid]; ok && d.conn != conn && d.lastValid.After(now) {
		return fmt.Sprintf("%s's write credentials stay valid until %s — %s can be granted then",
			d.conn, d.lastValid.Local().Format("15:04"), conn)
	}
	return ""
}

// GrantConflict is why a grant for connection conn of session sid would be
// refused right now because of another connection's window or leftover write
// credentials ("" = none). The grant panel shows it before a human tries.
func (b *Broker) GrantConflict(sid, conn string) string {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grantConflictLocked(sid, conn, now)
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
		identify(&row, sv, "")
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
	case !sv.Boxed:
		return refuse("this session has no AWS binding in a box")
	}
	bnd, entry, why := elevBinding(sv, req.Name)
	if why != "" {
		row.Entry = entry
		return refuse(why)
	}
	conn := req.Name // "" for the legacy binding
	if conn != "" {
		identify(&row, sv, conn)
	}
	switch {
	case bnd.Elevate == nil && conn != "":
		return refuse(fmt.Sprintf("connection %s did not allow elevation when the session started; add elevate to it in aws_connections and start a new session", conn))
	case bnd.Elevate == nil:
		return refuse("this session's profile did not allow elevation when it started; add elevate to the profile and start a new session")
	case !sv.Exclusive:
		return refuse("this session's box is shared; restart it to get its own box")
	}
	maxMin := bnd.Elevate.Max()
	if (req.Minutes != 30 && req.Minutes != 60) || req.Minutes > maxMin {
		return refuse(fmt.Sprintf("the window must be 30 or 60 minutes, at most %d", maxMin))
	}
	if !validReason(req.Reason) {
		return refuse("the reason must be one line of at most 200 characters")
	}
	if bnd.Prod && req.ConfirmLabel != bnd.Label {
		return refuse("type the binding label to grant write access on a production account")
	}
	b.mu.Lock() // checked again below, under the lock that grants; this spares the claim check
	conflict := b.grantConflictLocked(sid, conn, now)
	if conflict == "" {
		// The write hold (#325) starts before the claim check: a spawn into
		// the folder that has not passed its own check by now is refused, and
		// one that has is already running its box when the check lists them.
		// It ends when this returns, by which time a granted window holds it.
		b.granting[sid]++
		defer b.endGranting(sid)
	}
	b.mu.Unlock()
	if conflict != "" {
		return refuse(conflict)
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
	if conflict := b.grantConflictLocked(sid, conn, now); conflict != "" {
		b.mu.Unlock()
		return refuse(conflict)
	}
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
		e := &elevation{until: until, grantedAt: now, reason: row.Reason, conn: conn, row: row, gen: gen, pending: true}
		e.timer = b.after(until.Sub(now), func() { b.expire(sid, gen) })
		b.elev[sid] = e
		// A drop's leftover is cleared only when it is this connection's or its
		// credentials have expired: another connection's live leftover refused
		// the grant above. While WriteHold still holds it (the skew minute
		// included), its time moves onto the new window, so the folder hold and a
		// later drop keep it (#333).
		if d, ok := b.dropped[sid]; ok && (d.conn == conn || !d.lastValid.After(now)) {
			if d.lastValid.Add(dropHoldSkew).After(now) && d.lastValid.After(e.lastExp) {
				e.lastExp = d.lastValid
			}
			delete(b.dropped, sid)
		}
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
	ne := &elevation{until: until, grantedAt: cur.grantedAt, lastExp: cur.lastExp, reason: row.Reason, conn: cur.conn, row: row, gen: ng}
	ne.timer = b.after(until.Sub(b.now()), func() { b.expire(sid, ng) }) // the write took time: measure what is left now
	b.elev[sid] = ne
	return viewOf(ne), nil
}

func (b *Broker) endGranting(sid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.granting[sid]--; b.granting[sid] <= 0 {
		delete(b.granting, sid)
	}
}

// WriteHold is why session sid may hold AWS write credentials now, as the end
// of a sentence that starts with the session's name; "" = it holds none. The
// box layer refuses other boxes in the session's folder while it is not ""
// (#325): a grant in progress, a window (also one whose approval row is still
// being written), or write credentials from a dropped window that are still
// valid, also after the session was removed (#333: Forget keeps them, and a
// window open at removal counts as dropped then). adoptedAt, when set, is when the session's box was adopted after a
// restart: the broker has no record of windows from before it, and a write
// credential issued then may be valid for up to minElevatedRemaining after.
func (b *Broker) WriteHold(sid string, adoptedAt time.Time) string {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.elev[sid]; e != nil && !e.pending {
		return "has a write window open until " + e.until.Local().Format("15:04")
	}
	if b.elev[sid] != nil || b.granting[sid] > 0 {
		return "is opening a write window"
	}
	if d, ok := b.dropped[sid]; ok {
		if d.lastValid.Add(dropHoldSkew).After(now) {
			return "dropped its write window, but the write credentials it was given stay valid until " +
				d.lastValid.Add(dropHoldSkew).Local().Format("15:04") + " (with a minute for clock skew)"
		}
		delete(b.dropped, sid) // expired, margin included: a removed session's is asked about only here
	}
	if end := adoptedAt.Add(minElevatedRemaining); !adoptedAt.IsZero() && end.After(now) {
		return "was running when CuliBox or its box VM restarted, and write credentials it may have been given before then stay valid until " + end.Local().Format("15:04") + " at the latest"
	}
	return ""
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
	return ElevationView{Active: true, Until: e.until, GrantedAt: e.grantedAt, Reason: e.reason, Name: e.conn}
}

// endLocked removes a session's overlay (caller holds b.mu), stops its timer and
// remembers how long already-issued write credentials stay valid.
func (b *Broker) endLocked(sid string, e *elevation, now time.Time) {
	e.timer.Stop()
	delete(b.elev, sid)
	// Kept through the clock-skew margin too: WriteHold holds the folder for it.
	if e.lastExp.Add(dropHoldSkew).After(now) {
		b.dropped[sid] = droppedWin{conn: e.conn, lastValid: e.lastExp}
	}
}

// pruneDroppedLocked forgets every drop leftover whose credentials have
// expired, clock-skew margin included: a removed session's is never asked
// about again (caller holds b.mu).
func (b *Broker) pruneDroppedLocked(now time.Time) {
	for sid, d := range b.dropped {
		if !d.lastValid.Add(dropHoldSkew).After(now) {
			delete(b.dropped, sid)
		}
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
	b.endLocked(sid, e, b.now()) // a credential near the end stays held for the skew minute
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
	view := ElevationView{Name: e.conn}
	if d := b.dropped[sid]; d.lastValid.After(now) { // one kept only for the skew margin is not shown as valid
		view.LastValid = d.lastValid
	}
	b.mu.Unlock()
	row := e.row
	row.Time, row.Action, row.Until, row.Cred, row.Reason, row.Entry = now, ActionDropped, e.until, credLabel(cred), e.reason, e.conn
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
	if d, ok := b.dropped[sid]; ok {
		if d.lastValid.After(now) {
			return ElevationView{LastValid: d.lastValid, Name: d.conn}, true
		}
		if !d.lastValid.Add(dropHoldSkew).After(now) { // WriteHold still holds it for the skew margin
			delete(b.dropped, sid)
		}
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
	identify(&row, sv, "")
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
