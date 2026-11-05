package box

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// BoxName is what the session layer asks for before it builds the broker mount:
// it must be, key for key, the name Wrap then uses, or the socket it prepared
// would belong to a different box.
func TestBoxNameIsTheNameWrapUses(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	// Bound keys are fenced: they need the egress network.
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: okVerify})
	keys := map[string]BoxKey{
		"zero":            {},
		"account":         {Account: "work"},
		"binding":         {Binding: "h1"},
		"elevated":        {Binding: "h1", Session: "claude-7"},
		"context":         {Context: "c1"},
		"no imports":      {NoImports: true},
		"account+binding": {Account: "work", Binding: "h1"},
	}
	for label, k := range keys {
		t.Run(label, func(t *testing.T) {
			// One folder per key: an elevated key makes its box exclusive, and
			// Wrap refuses to share that folder with another session.
			dir := t.TempDir()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			want, err := m.BoxName(dir, k)
			if err != nil {
				t.Fatal(err)
			}
			_, _, got, err := m.Wrap(context.Background(), dir, "s", k, []string{"bash"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if want != got {
				t.Fatalf("BoxName = %s, Wrap used %s", want, got)
			}
			if !IsBoxName(want) {
				t.Fatalf("%s is not a box name", want)
			}
			// A symlinked spelling of the workspace is the same box.
			viaLink, err := m.BoxName(link, k)
			if err != nil || viaLink != want {
				t.Fatalf("BoxName through a symlink = %s, %v; want %s", viaLink, err, want)
			}
		})
	}
	if _, err := m.BoxName(filepath.Join(t.TempDir(), "missing"), BoxKey{}); err == nil {
		t.Fatal("BoxName of a workspace that does not exist must fail")
	}
}

func TestIsBoxName(t *testing.T) {
	for _, ok := range []string{"ab-box-0123456789ab"} {
		if !IsBoxName(ok) {
			t.Errorf("IsBoxName(%q) = false", ok)
		}
	}
	for _, bad := range []string{
		"", "ab-box-0123", "ab-box-0123456789AB", "../ab-box-0123456789ab", "ab-box-0123456789ab/x",
		"ab-box-0123456789abc", "ab-box-0123456789a", "xx-box-0123456789ab", "ab-box-0123456789ag",
	} {
		if IsBoxName(bad) {
			t.Errorf("IsBoxName(%q) = true", bad)
		}
	}
}
