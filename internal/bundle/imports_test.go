package bundle

import (
	"reflect"
	"testing"
)

func TestParseImports(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"line starts", "@~/.claude/context/projects.md\n@infra.md", []string{"~/.claude/context/projects.md", "infra.md"}},
		{"token runs to whitespace", "see @docs/a.md, then", []string{"docs/a.md,"}},
		{"inline code span", "add one `@~/.claude/context/<name>.md` line", []string{}},
		{"double-backtick span", "x ``a ` @b.md`` y", []string{}},
		{"unmatched backtick is literal", "x ` @b.md", []string{"b.md"}},
		{"backtick fence", "```\n@x.md\n```\n@y.md", []string{"y.md"}},
		{"tilde fence", "~~~\n@x.md\n~~~\n@y.md", []string{"y.md"}},
		{"fence info string", "```md\n@x.md\n```\n@y.md", []string{"y.md"}},
		{"longer fence not closed by shorter", "````\n```\n@x.md\n````\n@y.md", []string{"y.md"}},
		{"unclosed fence swallows rest", "```\n@x.md", []string{}},
		{"html comment inline", "<!-- @hidden.md -->\n@shown.md", []string{"shown.md"}},
		{"html comment multi-line", "<!--\n@hidden.md\n-->\n@shown.md", []string{"shown.md"}},
		{"text after comment close", "<!-- x\n--> @shown.md", []string{"shown.md"}},
		{"email address", "mail admin@culiops.dev", []string{}},
		{"escaped space", "@Design\\ Docs/api.md", []string{"Design Docs/api.md"}},
		{"double-quoted", "@\"quoted.md\"", []string{}},
		{"single-quoted", "@'quoted.md'", []string{}},
		{"bare at", "@", []string{}},
		{"bare at mid-line", "a @ b", []string{}},
		{"two on a line", "@a.md @b.md", []string{"a.md", "b.md"}},
		{"indented", "  @a.md", []string{"a.md"}},
		{"empty", "", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseImports([]byte(tc.in))
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseImports(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
