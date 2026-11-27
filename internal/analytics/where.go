package analytics

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxProfileRunes caps a `box:<name>` profile name in a where value.
const maxProfileRunes = 256

// Where narrows a query to one kind of session. Kind is "" (all), "host",
// "box", "boxp" (one box profile; Profile "" is the unnamed one) or
// "unknown" (sessions with no runtime recorded, e.g. legacy rollups).
type Where struct{ Kind, Profile string }

// ParseWhere reads the `where` request value. It accepts exactly host, box,
// unknown and box:<name> (0-256 runes, valid UTF-8, no control characters),
// case-sensitively. Anything else is "all", the way isDay treats a bad day.
func ParseWhere(q string) Where {
	switch q {
	case "host", "box", "unknown":
		return Where{Kind: q}
	}
	name, ok := strings.CutPrefix(q, "box:")
	if !ok || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxProfileRunes {
		return Where{}
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return Where{}
		}
	}
	return Where{Kind: "boxp", Profile: name}
}

// clause returns the SQL condition for w over a rollup table aliased r
// LEFT JOINed to session_dim d (see source). Every fragment is fixed text; the
// only bound value is the profile name. A zero Where has no condition.
func (w Where) clause() (string, []any) {
	switch w.Kind {
	case "host":
		return "d.runtime = 'host'", nil
	case "box":
		return "d.runtime = 'box'", nil
	case "boxp":
		return "d.runtime = 'box' AND d.profile = ?", []any{w.Profile}
	case "unknown":
		return "COALESCE(d.runtime, '') = ''", nil
	}
	return "", nil
}

// source returns the FROM text for a rollup table aliased r, and the extra
// " AND ..." condition with its bound args. A zero Where adds no join, so its
// query reads exactly the rows it did before the filter existed. The join is on
// the dim table's full primary key, so it cannot multiply rows.
func (w Where) source(table string) (from, and string, args []any) {
	c, args := w.clause()
	if c == "" {
		return table + " r", "", nil
	}
	return table + dimJoin, " AND (" + c + ")", args
}

// dimJoin is the LEFT JOIN of the rollup table (alias r) to session_dim (alias d).
const dimJoin = " r LEFT JOIN session_dim d ON d.session_id = r.session_id AND d.project = r.project"

// joined is source for a query that reads session_dim columns: the dim table is
// always joined, and the where condition (if any) is the one source adds.
func (w Where) joined(table string) (from, and string, args []any) {
	c, args := w.clause()
	if c == "" {
		return table + dimJoin, "", nil
	}
	return table + dimJoin, " AND (" + c + ")", args
}
