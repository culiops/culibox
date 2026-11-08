package auditlog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/mask"
)

// rawRow is one row of the UNION in the shared shape. What the slots hold
// depends on src (see the table definitions in auditlog.go):
//
//	credential: a=provider b=entry c=subject d=account_id e=role_arn f=outcome n1=elevated n2=repos n3=sso_direct
//	approval:   a=action   b=entry c=account_id d=profile e=reason f=cred n1=minutes
//	egress:     a=host     b=outcome            n1=port n2=bytes_down n3=suppressed
//	cred:       a=action b=kind c=name d=actor
//	store:      a=action b=actor c=detail
type rawRow struct {
	ts, id           int64
	src              int
	session          string
	a, b, c, d, e, f string
	n1, n2, n3       int64
}

// oneMB is where an egress row starts to show its download size.
const oneMB = 1 << 20

func describe(r rawRow) Event {
	// Mask every free-text slot first, before any word-splitting or
	// underscore handling can break a credential shape (a token-shaped name
	// like ghp_... must not become "ghp ..." and slip past the masker). The
	// slots hold identifiers, outcomes and free text alike; masking a plain
	// word is a no-op.
	r.a, r.b, r.c, r.d, r.e, r.f = mask.Display(r.a), mask.Display(r.b), mask.Display(r.c),
		mask.Display(r.d), mask.Display(r.e), mask.Display(r.f)
	ev := Event{TS: r.ts, Cursor: formatCursor(r.ts, r.src, r.id), Session: r.session}
	var parts []Part
	switch r.src {
	case srcCredential:
		ev.Kind, ev.Outcome, ev.Elevated = KindCredential, r.f, r.n1 != 0
		parts = credentialParts(r)
	case srcApproval:
		parts = approvalEvent(&ev, r)
	case srcEgress:
		ev.Kind, ev.Outcome = KindEgress, r.b
		parts = egressParts(r)
		if r.n3 > 0 {
			ev.Count = 1 + int(r.n3)
		}
	case srcCred:
		ev.Kind, ev.Outcome, ev.Actor = KindSettings, r.a, actorName(r.d)
		parts = settingsParts(r)
	case srcStore:
		ev.Kind, ev.Outcome, ev.Actor = KindStore, r.a, actorName(r.b)
		parts = storeParts(r)
	}
	ev.What = mask.Display(joinParts(parts)) // backstop; the slots were masked above
	ev.Parts = partsOrNil(parts, ev.What)
	if r.src == srcStore && r.a == "unlocked" {
		ev.Parts = plainTTL(ev.Parts, r.c) // after What is fixed: What keeps the stored detail
	}
	return ev
}

// txt is a plain part; ident is an identifier part (see Part).
func txt(s string) Part   { return Part{Text: s} }
func ident(s string) Part { return Part{Text: s, ID: true} }

// compact drops the parts with no text (a missing name leaves no part).
func compact(parts ...Part) []Part {
	var out []Part
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p)
		}
	}
	return out
}

// labelled is "<label> <name>" with the name an identifier; with no name it is
// the label alone, so nothing trails it.
func labelled(label, name string) []Part {
	if name == "" {
		return []Part{txt(label)}
	}
	return []Part{txt(label + " "), ident(name)}
}

// joinParts is the text the parts spell.
func joinParts(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// partsOrNil keeps the parts only while they still spell masked, the final
// What. The backstop masker can change a line that no single slot changed (a
// key's name in one slot and its value in the next): the parts would then show
// the unmasked text, so they are dropped and the page falls back to What.
func partsOrNil(parts []Part, masked string) []Part {
	if len(parts) == 0 || joinParts(parts) != masked {
		return nil
	}
	return parts
}

// credentialParts: "aws culipulse-prod · role agent-ops · elevated",
// "github culiops · 3 repos", "token datadog-read".
func credentialParts(r rawRow) []Part {
	switch r.a {
	case "github":
		p := labelled("github", first(r.c, r.b))
		if r.n2 > 0 {
			p = append(p, txt(" · "+strconv.FormatInt(r.n2, 10)+" "+plural(r.n2, "repo", "repos")))
		}
		return p
	case "static":
		return labelled("token", first(r.b, r.c))
	}
	p := labelled("aws", first(r.b, r.d))
	if role := roleName(r.e); role != "" {
		p = append(p, txt(" · role "), ident(role))
	}
	if r.n1 != 0 {
		p = append(p, txt(" · elevated"))
	}
	if r.n3 != 0 {
		p = append(p, txt(" · SSO role directly"))
	}
	return p
}

// approvalEvent fills an approval_audit row: an elevation, or a sign-in. It
// returns the parts of the What.
func approvalEvent(ev *Event, r rawRow) []Part {
	var p []Part
	switch r.a {
	case "sso_login_ok", "sso_login_failed":
		ev.Kind = KindSignIn
		ev.Outcome = map[string]string{"sso_login_ok": "ok", "sso_login_failed": "failed"}[r.a]
		p = labelled("SSO sign-in", first(r.b, r.d, r.c))
	default:
		ev.Kind, ev.Outcome = KindElevation, r.a
		p = compact(txt("write on "), ident(first(r.b, r.c, r.d)))
		if r.n1 > 0 {
			p = append(p, txt(" · "+strconv.FormatInt(r.n1, 10)+" min"))
		}
		ev.Reason = mask.Display(r.e)
	}
	switch r.f {
	case "cookie":
		ev.Actor = "you"
	case "bearer":
		ev.Actor = "API token"
	}
	return p
}

// egressParts: "host:port", then " · 14 MB down" from one megabyte up.
func egressParts(r rawRow) []Part {
	p := []Part{ident(r.a + ":" + strconv.FormatInt(r.n1, 10))}
	if r.n2 >= oneMB {
		p = append(p, txt(" · "+sizeMB(r.n2)+" down"))
	}
	return p
}

// settingsParts: "<kind> <name> <action>" ("token cloudflare-dns created"). The
// kind and action are fixed words; the name is free text and the identifier.
func settingsParts(r rawRow) []Part {
	kind, action := words(r.b), words(r.a)
	if r.c == "" {
		return compact(txt(joinNonEmpty(kind, action)))
	}
	lead, tail := "", ""
	if kind != "" {
		lead = kind + " "
	}
	if action != "" {
		tail = " " + action
	}
	return compact(txt(lead), ident(r.c), txt(tail))
}

// storeParts: "encrypted store <action>" and, when the row has one, " · <detail>".
// Only a value set or removed carries an identifier (the value id); any other
// detail is free text (an unlock's "ttl=...") and stays plain.
func storeParts(r rawRow) []Part {
	head := "encrypted store " + words(r.a)
	switch {
	case r.c == "":
		return []Part{txt(head)}
	case r.a == "value_set" || r.a == "value_removed":
		return []Part{txt(head + " · "), ident(r.c)}
	}
	return []Part{txt(head), txt(" · " + r.c)}
}

// plainTTL swaps the last part of an unlock's parts, " · ttl=168h0m0s", for
// " · for 7 days" when the detail is a positive ttl. A detail that is not one
// stays as stored. Only the parts change: What (and the CSV) keep the detail.
func plainTTL(parts []Part, detail string) []Part {
	raw, ok := strings.CutPrefix(detail, "ttl=")
	last := len(parts) - 1
	if !ok || last < 0 || parts[last].Text != " · "+detail {
		return parts
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return parts
	}
	parts[last] = txt(" · for " + humanDuration(d))
	return parts
}

// humanDuration is d in plain words: "7 days", "1 h 30 min", "30 s". Zero and
// negative read "0 s"; under a minute is whole seconds; otherwise days, hours
// and minutes with the zero units left out and the seconds dropped.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0 s"
	}
	if d < time.Minute {
		return strconv.Itoa(int(d/time.Second)) + " s"
	}
	mins := int64(d / time.Minute)
	days, hours, m := mins/(24*60), mins/60%24, mins%60
	var out []string
	if days > 0 {
		out = append(out, strconv.FormatInt(days, 10)+" "+plural(days, "day", "days"))
	}
	if hours > 0 {
		out = append(out, strconv.FormatInt(hours, 10)+" h")
	}
	if m > 0 {
		out = append(out, strconv.FormatInt(m, 10)+" min")
	}
	return strings.Join(out, " ")
}

// roleName is the last path segment of a role ARN.
func roleName(arn string) string {
	if i := strings.LastIndexByte(arn, '/'); i >= 0 {
		return arn[i+1:]
	}
	return ""
}

// first returns the first non-empty argument.
func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// words turns the underscores of a fixed action or kind word into spaces
// ("value_set" -> "value set"). Never use it on free text (names, hosts,
// details): that would reshape a credential before it is masked.
func words(w string) string { return strings.ReplaceAll(w, "_", " ") }

// joinNonEmpty joins the non-empty parts with spaces.
func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// actorName shows the web UI's own audit actor ("web", see web.storeActor) as
// "you"; any other actor is shown as recorded.
func actorName(a string) string {
	if a == "web" {
		return "you"
	}
	return a
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sizeMB renders a byte count of at least 1 MB: whole megabytes, gigabytes
// (one decimal) from 1024 MB.
func sizeMB(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MB", (b+oneMB/2)/oneMB)
}
