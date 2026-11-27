package analytics

import (
	"math"
	"strings"
	"testing"
)

func TestParseWhere(t *testing.T) {
	cases := []struct {
		in   string
		want Where
	}{
		{"", Where{}},
		{"host", Where{Kind: "host"}},
		{"box", Where{Kind: "box"}},
		{"unknown", Where{Kind: "unknown"}},
		{"box:prd", Where{Kind: "boxp", Profile: "prd"}},
		{"box:", Where{Kind: "boxp", Profile: ""}},
		{"box:héllo wörld", Where{Kind: "boxp", Profile: "héllo wörld"}},
		{"box:" + strings.Repeat("é", 256), Where{Kind: "boxp", Profile: strings.Repeat("é", 256)}},
		{"HOST", Where{}},
		{"Box", Where{}},
		{"all", Where{}},
		{"';drop", Where{}},
		{"host ", Where{}},
		{"box:" + strings.Repeat("a", 300), Where{}},
		{"box:" + strings.Repeat("é", 257), Where{}},
		{"box:a\nb", Where{}},
		{"box:a\x1b[31m", Where{}},
		{"box:a\x00b", Where{}},
		{"box:\xff\xfe", Where{}},
		{"BOX:prd", Where{}},
	}
	for _, c := range cases {
		if got := ParseWhere(c.in); got != c.want {
			t.Errorf("ParseWhere(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestWhereClauseIsFixedSQL(t *testing.T) {
	// The only bound value is the profile name; a hostile name never reaches the SQL text.
	hostile := "x' OR '1'='1"
	c, args := ParseWhere("box:" + hostile).clause()
	if strings.Contains(c, "OR") || strings.Contains(c, hostile) {
		t.Fatalf("clause carries request text: %q", c)
	}
	if len(args) != 1 || args[0] != hostile {
		t.Fatalf("args = %v, want the profile bound once", args)
	}
	if c, args := (Where{}).clause(); c != "" || args != nil {
		t.Fatalf("zero Where clause = %q %v, want none", c, args)
	}
}

// whereFixture inserts one host session ($1), one box "prd" session ($2), one
// unnamed box session ($4) and one legacy session with no session_dim row ($8).
// Each session also has attribution records equal to its cost.
func whereFixture(t *testing.T, s *Store) {
	t.Helper()
	sess := []struct {
		id, runtime, profile string
		hasDim               bool
		cost                 float64
	}{
		{"H", "host", "", true, 1},
		{"BP", "box", "prd", true, 2},
		{"BU", "box", "", true, 4},
		{"L", "", "", false, 8},
	}
	for _, x := range sess {
		if _, err := s.db.Exec(`INSERT INTO session_rollup(day,session_id,project,model,input_tokens,prompts,cost_usd) VALUES('2026-08-05',?,'p','m',10,1,?)`,
			x.id, x.cost); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO attribution_rollup(day,session_id,project,kind,name,records) VALUES('2026-08-05',?,'p','skill',?,?)`,
			x.id, "sk-"+x.id, int64(x.cost)); err != nil {
			t.Fatal(err)
		}
		if x.hasDim {
			if _, err := s.db.Exec(`INSERT INTO session_dim(session_id,project,runtime,profile) VALUES(?,'p',?,?)`,
				x.id, x.runtime, x.profile); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWhereFilterEachQuery(t *testing.T) {
	s := openTemp(t)
	whereFixture(t, s)
	f, to := "2026-08-01", "2026-08-31"

	sums := map[string]func(Where) float64{
		"ByDayModel": func(w Where) float64 {
			rows, err := s.ByDayModel(f, to, w)
			if err != nil {
				t.Fatal(err)
			}
			var n float64
			for _, r := range rows {
				n += r.Cost
			}
			return n
		},
		"ByModel": func(w Where) float64 {
			rows, err := s.ByModel(f, to, w)
			if err != nil {
				t.Fatal(err)
			}
			var n float64
			for _, r := range rows {
				n += r.Cost
			}
			return n
		},
		"ByProject": func(w Where) float64 {
			rows, err := s.ByProject(f, to, w)
			if err != nil {
				t.Fatal(err)
			}
			var n float64
			for _, r := range rows {
				n += r.Cost
			}
			return n
		},
		"Sessions": func(w Where) float64 {
			rows, err := s.Sessions(f, to, 100, w)
			if err != nil {
				t.Fatal(err)
			}
			var n float64
			for _, r := range rows {
				n += r.Cost
			}
			return n
		},
		"SessionCosts": func(w Where) float64 {
			rows, err := s.SessionCosts(f, to, w)
			if err != nil {
				t.Fatal(err)
			}
			var n float64
			for _, r := range rows {
				n += r.Cost
			}
			return n
		},
		// Attribution records equal the session cost in the fixture.
		"Tools": func(w Where) float64 {
			rows, err := s.Tools(f, to, 100, w)
			if err != nil {
				t.Fatal(err)
			}
			var n float64
			for _, r := range rows {
				n += float64(r.Records)
			}
			return n
		},
	}
	want := []struct {
		name string
		w    Where
		sum  float64
	}{
		{"all", Where{}, 15},
		{"host", ParseWhere("host"), 1},
		{"box", ParseWhere("box"), 6},
		{"box:prd", ParseWhere("box:prd"), 2},
		{"box:", ParseWhere("box:"), 4},
		{"unknown", ParseWhere("unknown"), 8},
	}
	for q, fn := range sums {
		for _, c := range want {
			if got := fn(c.w); math.Abs(got-c.sum) > 1e-9 {
				t.Errorf("%s where=%s: sum = %v, want %v", q, c.name, got, c.sum)
			}
		}
		h, b, u, all := fn(ParseWhere("host")), fn(ParseWhere("box")), fn(ParseWhere("unknown")), fn(Where{})
		if math.Abs(h+b+u-all) > 1e-9 {
			t.Errorf("%s: host+box+unknown = %v, All = %v", q, h+b+u, all)
		}
	}
}

// The join is on (session_id, project): the same session id under another
// project must neither inherit a dim row nor multiply rows.
func TestWhereJoinKeysOnProject(t *testing.T) {
	s := openTemp(t)
	whereFixture(t, s)
	// "H" under project "q" has no dim row of its own: unknown, not host.
	if _, err := s.db.Exec(`INSERT INTO session_rollup(day,session_id,project,model,cost_usd) VALUES('2026-08-06','H','q','m',16)`); err != nil {
		t.Fatal(err)
	}
	cost := func(w Where) float64 {
		rows, err := s.SessionCosts("2026-08-01", "2026-08-31", w)
		if err != nil {
			t.Fatal(err)
		}
		var n float64
		for _, r := range rows {
			n += r.Cost
		}
		return n
	}
	if got := cost(ParseWhere("host")); got != 1 {
		t.Errorf("host = %v, want 1", got)
	}
	if got := cost(ParseWhere("unknown")); got != 24 {
		t.Errorf("unknown = %v, want 24", got)
	}
	if got := cost(Where{}); got != 31 {
		t.Errorf("all = %v, want 31", got)
	}
}
