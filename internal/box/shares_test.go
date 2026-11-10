package box

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestShareViewCheck(t *testing.T) {
	v := ShareView{
		Applied: []VMShare{{Path: "/h/code", Writable: true}, {Path: "/h/ref"}},
		Listed:  []VMShare{{Path: "/h/code", Writable: true}, {Path: "/h/ref"}, {Path: "/h/code/newproj", Writable: true}},
		Home:    "/h",
	}
	notShared := "~/other is not shared with the box VM, so a box can’t use it. Add it in Settings › VM folders, or run `agent-box box share add ~/other`"
	cases := []struct {
		name  string
		view  ShareView
		abs   string
		write bool
		want  string // "" = nil
	}{
		{"rw under rw", v, "/h/code/x", true, ""},
		{"ro under ro, read", v, "/h/ref/x", false, ""},
		{"not shared", v, "/h/other", true, notShared},
		{"read only", v, "/h/ref/x", true,
			"~/ref/x is shared read-only with the box VM, but a workspace needs read & write. Switch it in Settings › VM folders, or run `agent-box box share add ~/ref/x`"},
		{"pending", ShareView{Applied: []VMShare{{Path: "/h/ref"}}, Listed: []VMShare{{Path: "/h/code"}}, Home: "/h"}, "/h/code/newproj", true,
			"~/code/newproj was added to the box VM folders but not applied yet: apply it in Settings › VM folders (this restarts the box VM), or run `agent-box box share apply`"},
		{"no fold", v, "/h/Code/x", true, "~/Code/x is not shared with the box VM, so a box can’t use it. Add it in Settings › VM folders, or run `agent-box box share add ~/Code/x`"},
		{"fold", ShareView{Applied: v.Applied, Home: "/h", Fold: true}, "/h/Code/x", true, ""},
	}
	for _, c := range cases {
		err := c.view.Check(c.abs, c.write)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		var ns *NotSharedError
		if !errors.As(err, &ns) || !errors.Is(err, ErrUnboxableWorkspace) || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	if got := (&NotSharedError{Dir: "/h/code/newproj", Home: "/h"}).Error(); got == "" {
		t.Error("empty")
	}
	if !errors.Is(ErrVMRestarting, ErrBoxUnavailable) {
		t.Error("ErrVMRestarting must be an ErrBoxUnavailable")
	}
}

func sharesMgr(t *testing.T, fn SharesFunc) *Manager {
	t.Helper()
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: newFake(), Shares: fn})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUnboxableReturnsNotSharedError(t *testing.T) {
	shared := realTemp(t)
	m := sharesMgr(t, func() (ShareView, error) { return ShareView{Applied: []VMShare{{Path: shared, Writable: true}}}, nil })
	_, _, _, err := m.Wrap(context.Background(), realTemp(t), "s-1", BoxKey{}, []string{"claude"}, nil, nil)
	var ns *NotSharedError
	if !errors.As(err, &ns) || !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("want *NotSharedError wrapping ErrUnboxableWorkspace, got %v", err)
	}
}

func TestUnboxableSharesFuncErrorFailsClosed(t *testing.T) {
	m := sharesMgr(t, func() (ShareView, error) { return ShareView{}, errors.New("boom") })
	_, _, _, err := m.Wrap(context.Background(), realTemp(t), "s-1", BoxKey{}, []string{"claude"}, nil, nil)
	if !errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if reason, bad := m.Unboxable(realTemp(t)); !bad || reason == "" {
		t.Fatalf("Unboxable must refuse, got %q %v", reason, bad)
	}
}

func TestOptionsSharesNilMeansNoCheck(t *testing.T) {
	m := sharesMgr(t, nil)
	if reason, bad := m.Unboxable(realTemp(t)); bad {
		t.Fatalf("nil Shares must not refuse: %q", reason)
	}
}

func TestUnboxableIgnoringSharesSkipsTheShareRule(t *testing.T) {
	m := sharesMgr(t, func() (ShareView, error) { return ShareView{}, errors.New("boom") })
	ws := realTemp(t)
	if reason, bad := m.Unboxable(ws); !bad || reason == "" {
		t.Fatalf("Unboxable must refuse on a share failure, got %q %v", reason, bad)
	}
	if reason, bad := m.UnboxableIgnoringShares(ws); bad {
		t.Fatalf("UnboxableIgnoringShares must ignore the share rule, got %q", reason)
	}
	if _, bad := m.UnboxableIgnoringShares(m.opts.StateRoot); !bad {
		t.Fatal("the other rules still apply")
	}
}

// C2: a folder a box only reads (clone_of, a context path) is offered as a
// read-only share; one a box writes as a read-write one.
func TestNotSharedHintFollowsTheNeededAccess(t *testing.T) {
	v := ShareView{Applied: []VMShare{{Path: "/h/code", Writable: true}}, Home: "/h"}
	err := v.Check("/h/orig", false)
	var ns *NotSharedError
	if !errors.As(err, &ns) || ns.NeedWrite {
		t.Fatalf("read-only need: %v (%+v)", err, ns)
	}
	if want := "~/orig is not shared with the box VM, so a box can’t use it. Add it in Settings › VM folders, or run `agent-box box share add ~/orig -ro`"; err.Error() != want {
		t.Errorf("got  %q\nwant %q", err, want)
	}
	err = v.Check("/h/other", true)
	if !errors.As(err, &ns) || !ns.NeedWrite || strings.Contains(err.Error(), "-ro") {
		t.Errorf("write need: %v (%+v)", err, ns)
	}
}
