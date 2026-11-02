package auditlog

import (
	"fmt"
	"strconv"
	"strings"

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
	switch r.src {
	case srcCredential:
		ev.Kind, ev.What, ev.Outcome, ev.Elevated = KindCredential, credentialWhat(r), r.f, r.n1 != 0
	case srcApproval:
		approvalEvent(&ev, r)
	case srcEgress:
		ev.Kind, ev.Outcome = KindEgress, r.b
		ev.What = r.a + ":" + strconv.FormatInt(r.n1, 10)
		if r.n2 >= oneMB {
			ev.What += " · " + sizeMB(r.n2) + " down"
		}
		if r.n3 > 0 {
			ev.Count = 1 + int(r.n3)
		}
	case srcCred:
		ev.Kind, ev.Outcome, ev.Actor = KindSettings, r.a, actorName(r.d)
		ev.What = joinNonEmpty(words(r.b), r.c, words(r.a)) // kind and action are fixed words; the name is free text
	case srcStore:
		ev.Kind, ev.Outcome, ev.Actor = KindStore, r.a, actorName(r.b)
		ev.What = "encrypted store " + words(r.a)
		if r.c != "" {
			ev.What += " · " + r.c
		}
	}
	ev.What = mask.Display(ev.What) // backstop; the slots were masked above
	return ev
}

// credentialWhat: "aws culipulse-prod · role agent-ops · elevated",
// "github culiops · 3 repos", "token datadog-read".
func credentialWhat(r rawRow) string {
	switch r.a {
	case "github":
		w := "github " + first(r.c, r.b)
		if r.n2 > 0 {
			w += " · " + strconv.FormatInt(r.n2, 10) + " " + plural(r.n2, "repo", "repos")
		}
		return strings.TrimSpace(w)
	case "static":
		return strings.TrimSpace("token " + first(r.b, r.c))
	}
	w := strings.TrimSpace("aws " + first(r.b, r.d))
	if role := roleName(r.e); role != "" {
		w += " · role " + role
	}
	if r.n1 != 0 {
		w += " · elevated"
	}
	if r.n3 != 0 {
		w += " · SSO role directly"
	}
	return w
}

// approvalEvent fills an approval_audit row: an elevation, or a sign-in.
func approvalEvent(ev *Event, r rawRow) {
	switch r.a {
	case "sso_login_ok", "sso_login_failed":
		ev.Kind = KindSignIn
		ev.Outcome = map[string]string{"sso_login_ok": "ok", "sso_login_failed": "failed"}[r.a]
		ev.What = strings.TrimSpace("SSO sign-in " + first(r.b, r.d, r.c))
	default:
		ev.Kind, ev.Outcome = KindElevation, r.a
		ev.What = "write on " + first(r.b, r.c, r.d)
		if r.n1 > 0 {
			ev.What += " · " + strconv.FormatInt(r.n1, 10) + " min"
		}
		ev.Reason = mask.Display(r.e)
	}
	switch r.f {
	case "cookie":
		ev.Actor = "you"
	case "bearer":
		ev.Actor = "API token"
	}
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
