package awsprobe

import (
	"strings"
	"testing"
	"time"
)

const ackCaller = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Example_0000000000000000"

func verdicts(p ...string) []Verdict {
	var out []Verdict
	for _, s := range p {
		out = append(out, Verdict{Pattern: s, Why: "why"})
	}
	return out
}

func nro(role string, pats ...string) GateResult {
	return GateResult{State: GateNotReadOnly, RoleARN: role, Caller: ackCaller, Refused: verdicts(pats...)}
}

func unv(reason Reason, role string, pats []string, perms []string) GateResult {
	return GateResult{State: GateUnverified, Reason: reason, RoleARN: role, Caller: ackCaller, Refused: verdicts(pats...), MissingPerms: perms}
}

func ackOf(res GateResult) *Ack {
	a := NewAck(res, "web", time.Unix(1700000000, 0).UTC())
	return &a
}

func TestAdmitSSO(t *testing.T) {
	const role = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_Example_0000000000000000"
	both := nro(role, "iam:*", "s3:PutObject")
	grant := []string{"iam:GetRole", "iam:GetPolicy"}
	mp := unv(ReasonMissingPerms, "", nil, grant)
	mc := unv(ReasonMissingContext, role, []string{"a:A"}, nil)
	other := nro(role, "iam:*", "s3:PutObject")
	other.Caller = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Other_1111111111111111"
	noCaller := nro(role, "iam:*")
	noCaller.Caller = ""
	ro := GateResult{State: GateReadOnly, RoleARN: role, Caller: ackCaller}
	roNoRole := GateResult{State: GateReadOnly, Caller: ackCaller}

	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sd := unv(ReasonSimulateDenied, role, nil, nil)
	sd.PolicyDigest = digest
	sdOther := sd
	sdOther.PolicyDigest = strings.Repeat("a", 64)
	sdNoDigest := sd
	sdNoDigest.PolicyDigest = ""
	sdNoRole := sd
	sdNoRole.RoleARN = ""
	// What the gate really returns when step 5 refused patterns and the
	// simulation then stopped: not_read_only, Incomplete says why.
	partial := func(why Reason, d string, pats ...string) GateResult {
		r := nro(role, pats...)
		r.Incomplete, r.PolicyDigest = why, d
		return r
	}
	sdPats := partial(ReasonSimulateDenied, digest, "s3:Put*", "iam:*")
	sdNarrower := partial(ReasonSimulateDenied, digest, "s3:Put*")
	sdPatsOther := partial(ReasonSimulateDenied, strings.Repeat("a", 64), "s3:Put*")
	sdPatsNoDigest := partial(ReasonSimulateDenied, "", "s3:Put*")
	sdPatsPseudo := partial(ReasonSimulateDenied, digest, "s3:Put*")
	sdPatsPseudo.Refused = verdicts("s3:Put*", "account")
	full := nro(role, "s3:Put*", "iam:*")
	full.PolicyDigest = digest

	rows := []struct {
		name string
		res  GateResult
		ack  *Ack
		want Admission
	}{
		{"simulate_denied no ack", sd, nil, Admission{Eligible: true}},
		{"simulate_denied acked", sd, ackOf(sd), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"simulate_denied digest changed", sdOther, ackOf(sd), Admission{Eligible: true, Stale: true}},
		{"simulate_denied empty digest", sdNoDigest, nil, Admission{}},
		{"simulate_denied empty digest with ack", sdNoDigest, ackOf(sd), Admission{}},
		{"simulate_denied empty role", sdNoRole, nil, Admission{}},
		{"partial simulate_denied no ack", sdPats, nil, Admission{Eligible: true}},
		{"partial simulate_denied acked", sdPats, ackOf(sdPats), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"partial simulate_denied narrower same digest", sdNarrower, ackOf(sdPats), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"partial simulate_denied digest changed", sdPatsOther, ackOf(sdPats), Admission{Eligible: true, Stale: true}},
		{"partial simulate_denied empty digest", sdPatsNoDigest, nil, Admission{}},
		{"partial simulate_denied empty digest with ack", sdPatsNoDigest, ackOf(sdPats), Admission{}},
		{"partial simulate_denied pseudo pattern", sdPatsPseudo, nil, Admission{}},
		{"partial simulate_denied wider than unverified ack", sdPats, ackOf(sd), Admission{Eligible: true, Stale: true}},
		{"partial simulate_denied under a full-run ack", sdPats, ackOf(nro(role, "s3:Put*", "iam:*")), Admission{Eligible: true, Stale: true}},
		{"partial simulate_denied under a full-run structural ack", partial(ReasonSimulateDenied, digest, "notaction"), ackOf(func() GateResult { r := nro(role, "notaction"); r.PolicyDigest = digest; return r }()), Admission{Eligible: true, Stale: true}},
		{"full run under a partial simulate_denied ack same digest", full, ackOf(sdPats), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"full run under a partial simulate_denied ack other digest", nro(role, "s3:Put*"), ackOf(sdPats), Admission{Eligible: true, Stale: true}},
		{"missing perms ack does not cover simulate_denied", sd, ackOf(mp), Admission{Eligible: true, Stale: true}},
		{"read only", ro, nil, Admission{OK: true}},
		{"read only without role", roNoRole, nil, Admission{}},
		{"not read only no ack", both, nil, Admission{Eligible: true}},
		{"same set acked", both, ackOf(both), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"ack narrower than current", both, ackOf(nro(role, "iam:*")), Admission{Eligible: true, Stale: true}},
		{"current narrower than ack", nro(role, "iam:*"), ackOf(both), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"case", nro(role, "IAM:*"), ackOf(nro(role, "iam:*")), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"missing perms no ack", mp, nil, Admission{Eligible: true}},
		{"missing perms acked", mp, ackOf(mp), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"missing perms under breadth ack", mp, ackOf(nro(role, "iam:*")), Admission{Eligible: true, Stale: true}},
		{"breadth under missing perms ack", nro(role, "x:*"), ackOf(mp), Admission{Eligible: true, Stale: true}},
		{"missing context under breadth ack", mc, ackOf(nro(role, "a:A", "b:B")), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"missing context no ack", mc, nil, Admission{Eligible: true}},
		{"different caller", other, ackOf(both), Admission{Eligible: true, Stale: true}},
		{"empty caller", noCaller, nil, Admission{}},
		{"empty caller with ack", noCaller, ackOf(nro(role, "iam:*")), Admission{}},
	}
	for _, r := range []Reason{ReasonSSOExpired, ReasonTimeout, ReasonError, ReasonAssumeDenied, ReasonProbeDisabled, ReasonNotChecked} {
		res := unv(r, role, nil, nil)
		rows = append(rows, struct {
			name string
			res  GateResult
			ack  *Ack
			want Admission
		}{"transient " + string(r), res, ackOf(both), Admission{}})
	}
	// A run cut short by a transient failure is never admitted, even when
	// step 5 found refusals that a held ack covers.
	for _, r := range []Reason{ReasonError, ReasonTimeout, ReasonSSOExpired} {
		res := partial(r, digest, "iam:*")
		for name, a := range map[string]*Ack{"none": nil, "breadth": ackOf(nro(role, "iam:*", "s3:Put*")), "partial": ackOf(sdPats)} {
			rows = append(rows, struct {
				name string
				res  GateResult
				ack  *Ack
				want Admission
			}{"partial " + string(r) + " ack " + name, res, a, Admission{}})
		}
	}
	for _, p := range []string{PatternCaller, PatternAccount, PatternRole, PatternSourceProfile} {
		for _, r := range []string{"", role} {
			res := nro(r, p)
			res.Refused = verdicts(p, "iam:*")
			a := Ack{State: GateNotReadOnly, Caller: ackCaller, Refused: []string{p, "iam:*"}}
			rows = append(rows, struct {
				name string
				res  GateResult
				ack  *Ack
				want Admission
			}{"identity " + p + " role=" + r, res, &a, Admission{}})
		}
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := AdmitSSO(row.res, row.ack); got != row.want {
				t.Fatalf("AdmitSSO = %+v, want %+v", got, row.want)
			}
		})
	}
}

func TestNewAckNormalises(t *testing.T) {
	res := nro("arn:aws:iam::123456789012:role/x", "S3:PutObject", "iam:*", "IAM:*")
	res.MissingPerms = []string{"iam:GetRole", "a:b", "iam:GetRole"}
	at := time.Unix(1700000000, 0).UTC()
	a := NewAck(res, "web", at)
	if strings.Join(a.Refused, ",") != "iam:*,s3:putobject" {
		t.Errorf("refused %v", a.Refused)
	}
	if strings.Join(a.MissingPerms, ",") != "a:b,iam:GetRole" {
		t.Errorf("missing %v", a.MissingPerms)
	}
	if a.Actor != "web" || !a.At.Equal(at) || a.Caller != ackCaller || a.State != GateNotReadOnly {
		t.Errorf("ack %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestAckValidate(t *testing.T) {
	good := Ack{State: GateNotReadOnly, Caller: ackCaller, Refused: []string{"iam:*"}, Actor: "web"}
	if err := good.Validate(); err != nil {
		t.Fatalf("good: %v", err)
	}
	gu := Ack{State: GateUnverified, Reason: ReasonMissingPerms, Caller: ackCaller, MissingPerms: []string{"iam:GetRole"}}
	if err := gu.Validate(); err != nil {
		t.Fatalf("good unverified: %v", err)
	}
	many := make([]string, 4097)
	for i := range many {
		many[i] = "a:b"
	}
	bad := map[string]func(a *Ack){
		"state":         func(a *Ack) { a.State = GateReadOnly },
		"empty state":   func(a *Ack) { a.State = "" },
		"reason on nr":  func(a *Ack) { a.Reason = ReasonMissingPerms },
		"unv reason":    func(a *Ack) { a.State, a.Reason = GateUnverified, ReasonTimeout },
		"unv none":      func(a *Ack) { a.State, a.Reason = GateUnverified, "" },
		"caller":        func(a *Ack) { a.Caller = "" },
		"too many":      func(a *Ack) { a.Refused = many },
		"too many mp":   func(a *Ack) { a.MissingPerms = many },
		"long item":     func(a *Ack) { a.Refused = []string{strings.Repeat("x", 257)} },
		"control":       func(a *Ack) { a.Refused = []string{"iam:*\n"} },
		"control mp":    func(a *Ack) { a.MissingPerms = []string{"a\x00b"} },
		"control actor": func(a *Ack) { a.Actor = "w\x1b[0m" },
		"pseudo":        func(a *Ack) { a.Refused = []string{"account"} },
	}
	for name, mut := range bad {
		a := good
		mut(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestAckSummaryBounded(t *testing.T) {
	pats := make([]string, 300)
	for i := range pats {
		pats[i] = "service" + strings.Repeat("x", 5) + ":Action" + strings.Repeat("y", i%7)
	}
	a := Ack{State: GateNotReadOnly, Caller: ackCaller, Refused: pats}
	s := a.Summary()
	if len(s) > 200 {
		t.Fatalf("len %d: %s", len(s), s)
	}
	if !strings.HasSuffix(s, "(300 refused)") || !strings.HasPrefix(s, "not_read_only") {
		t.Errorf("summary %q", s)
	}
	short := Ack{State: GateNotReadOnly, Caller: ackCaller, Refused: []string{"iam:*"}}.Summary()
	if !strings.Contains(short, "iam:*") {
		t.Errorf("short %q", short)
	}
}

func TestAckStructuralPinnedToPolicyDigest(t *testing.T) {
	const role = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_Example_0000000000000000"
	d1 := strings.Repeat("a", 64)
	d2 := strings.Repeat("b", 64)
	mk := func(digest string, pats ...string) GateResult {
		r := nro(role, pats...)
		r.PolicyDigest = digest
		return r
	}
	ack := ackOf(mk(d1, "NotAction"))
	if ack.PolicyDigest != d1 {
		t.Fatalf("ack digest %q", ack.PolicyDigest)
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rows := []struct {
		name string
		res  GateResult
		want Admission
	}{
		{"same digest", mk(d1, "notaction"), Admission{OK: true, UnderAck: true, Eligible: true}},
		{"different digest", mk(d2, "NotAction"), Admission{Eligible: true, Stale: true}},
		{"empty digest", mk("", "NotAction"), Admission{Eligible: true, Stale: true}},
	}
	for _, r := range rows {
		if got := AdmitSSO(r.res, ack); got != r.want {
			t.Errorf("%s: %+v, want %+v", r.name, got, r.want)
		}
	}
	// A structural pattern the ack never held is not covered at all.
	if got := AdmitSSO(mk(d1, "Action"), ack); !got.Stale {
		t.Errorf("Action under a NotAction ack: %+v", got)
	}
	// A non-structural ack ignores the digest, and carries none.
	plain := ackOf(mk(d1, "iam:*", "s3:PutObject"))
	if plain.PolicyDigest != "" {
		t.Errorf("plain ack digest %q", plain.PolicyDigest)
	}
	if got := AdmitSSO(mk(d2, "iam:*"), plain); !got.OK {
		t.Errorf("narrowing with another digest: %+v", got)
	}
	if got := AdmitSSO(mk("", "iam:*"), plain); !got.OK {
		t.Errorf("narrowing with no digest: %+v", got)
	}
	// Validate.
	for name, a := range map[string]Ack{
		"missing": {State: GateNotReadOnly, Caller: ackCaller, Refused: []string{"notaction"}},
		"short":   {State: GateNotReadOnly, Caller: ackCaller, Refused: []string{"action"}, PolicyDigest: "abc"},
		"upper":   {State: GateNotReadOnly, Caller: ackCaller, Refused: []string{"action"}, PolicyDigest: strings.Repeat("A", 64)},
		"stray":   {State: GateNotReadOnly, Caller: ackCaller, Refused: []string{"iam:*"}, PolicyDigest: d1},
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSimulateDeniedAckPinsDigest(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	res := unv(ReasonSimulateDenied, "arn:aws:iam::123456789012:role/x", nil, nil)
	res.PolicyDigest = digest
	a := NewAck(res, "web", time.Unix(1700000000, 0).UTC())
	if a.PolicyDigest != digest || a.Reason != ReasonSimulateDenied {
		t.Fatalf("%+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"", "short", strings.Repeat("G", 64)} {
		b := a
		b.PolicyDigest = d
		if b.Validate() == nil {
			t.Errorf("digest %q accepted", d)
		}
	}
	m := NewAck(unv(ReasonMissingPerms, "", nil, []string{"iam:GetRole"}), "web", time.Time{})
	m.PolicyDigest = digest
	if m.Validate() == nil {
		t.Error("a stray digest on a missing_permissions ack was accepted")
	}
}

func TestAckIncompleteSimulateDenied(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	res := nro("arn:aws:iam::123456789012:role/x", "s3:Put*")
	res.Incomplete, res.PolicyDigest = ReasonSimulateDenied, digest
	a := NewAck(res, "web", time.Unix(1700000000, 0).UTC())
	if a.Incomplete != ReasonSimulateDenied || a.PolicyDigest != digest || a.Reason != "" || a.State != GateNotReadOnly {
		t.Fatalf("%+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Summary(), "simulate_denied") {
		t.Errorf("summary %q", a.Summary())
	}
	// A full run's ack carries neither Incomplete nor a stray digest.
	res.Incomplete = ""
	if b := NewAck(res, "web", time.Time{}); b.Incomplete != "" || b.PolicyDigest != "" {
		t.Errorf("full-run ack %+v", b)
	}
	bad := map[string]func(a *Ack){
		"no digest":     func(a *Ack) { a.PolicyDigest = "" },
		"short digest":  func(a *Ack) { a.PolicyDigest = "abc" },
		"transient":     func(a *Ack) { a.Incomplete = ReasonTimeout },
		"error":         func(a *Ack) { a.Incomplete = ReasonError },
		"on unverified": func(a *Ack) { a.State, a.Reason = GateUnverified, ReasonSimulateDenied },
		"on missing perms": func(a *Ack) {
			a.State, a.Reason, a.MissingPerms = GateUnverified, ReasonMissingPerms, []string{"iam:GetRole"}
		},
	}
	for name, mut := range bad {
		b := a
		mut(&b)
		if b.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
