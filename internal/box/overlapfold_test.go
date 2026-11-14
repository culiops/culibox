package box

import "testing"

// TestOverlapsFold pins the case folding the darwin build uses (#298), so Linux
// CI covers it. fold=false is the Linux behaviour and must not change.
func TestOverlapsFold(t *testing.T) {
	cases := []struct {
		name       string
		a, b       string
		fold, want bool
	}{
		{"same spelling", "/a/proj", "/a/proj", false, true},
		{"other case, no fold", "/a/Proj", "/a/proj", false, false},
		{"other case, fold", "/a/Proj", "/a/proj", true, true},
		{"child of other case, fold", "/a/Proj/sub", "/a/proj", true, true},
		{"parent of other case, fold", "/a/PROJ", "/a/proj/sub/deep", true, true},
		{"child of other case, no fold", "/a/Proj/sub", "/a/proj", false, false},
		{"case in a parent component, fold", "/A/proj", "/a/proj/x", true, true},
		{"sibling prefix, fold", "/a/proj2", "/a/proj", true, false},
		{"sibling prefix other case, fold", "/a/PROJ2", "/a/proj", true, false},
		{"unrelated, fold", "/a/other", "/a/proj", true, false},
		{"trailing slash and dots, fold", "/a/Proj/./", "/a/x/../proj", true, true},
		{"root contains all", "/", "/A/b", true, true},
	}
	for _, c := range cases {
		if got := overlaps(c.a, c.b, c.fold); got != c.want {
			t.Errorf("%s: overlaps(%q, %q, fold=%v) = %v, want %v", c.name, c.a, c.b, c.fold, got, c.want)
		}
		if got := overlaps(c.b, c.a, c.fold); got != c.want {
			t.Errorf("%s (swapped): overlaps(%q, %q, fold=%v) = %v, want %v", c.name, c.b, c.a, c.fold, got, c.want)
		}
	}
}
