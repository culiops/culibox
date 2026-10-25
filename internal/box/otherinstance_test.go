package box

import "testing"

func TestOtherInstanceHolds(t *testing.T) {
	const dir = "/a/clone"
	cases := []struct {
		name string
		box  BoxInfo
		busy bool
	}{
		{"other instance at the dir", BoxInfo{Name: "x", State: "running", Workspace: "/a/clone", Instance: "PROD"}, true},
		{"other instance below the dir", BoxInfo{Name: "x", State: "running", Workspace: "/a/clone/sub", Instance: "PROD"}, true},
		{"other instance above the dir", BoxInfo{Name: "x", State: "running", Workspace: "/a", Instance: "PROD"}, true},
		{"other instance paused", BoxInfo{Name: "x", State: "paused", Workspace: "/a/clone", Instance: "PROD"}, true},
		{"same instance (idle own box)", BoxInfo{Name: "x", State: "running", Workspace: "/a/clone", Instance: "DEV"}, false},
		{"other instance exited", BoxInfo{Name: "x", State: "exited", Workspace: "/a/clone", Instance: "PROD"}, false},
		{"unreadable row", BoxInfo{Name: "x", State: "running", Workspace: "/b", Instance: "DEV", Unreadable: true}, true},
		{"no workspace label", BoxInfo{Name: "x", State: "running", Workspace: "", Instance: "DEV"}, true},
		{"relative workspace label", BoxInfo{Name: "x", State: "running", Workspace: "rel", Instance: "PROD"}, true},
		{"other instance elsewhere", BoxInfo{Name: "x", State: "running", Workspace: "/b", Instance: "PROD"}, false},
		{"other instance at a sibling prefix", BoxInfo{Name: "x", State: "running", Workspace: "/a/clone2", Instance: "PROD"}, false},
	}
	for _, c := range cases {
		holder, busy := OtherInstanceHolds([]BoxInfo{c.box}, dir, "DEV")
		if busy != c.busy {
			t.Errorf("%s: busy = %v, want %v", c.name, busy, c.busy)
		}
		if busy && holder != c.box.Name {
			t.Errorf("%s: holder = %q, want %q", c.name, holder, c.box.Name)
		}
		if !busy && holder != "" {
			t.Errorf("%s: free but holder = %q", c.name, holder)
		}
	}

	// The first holder is named; free rows before it are passed over.
	boxes := []BoxInfo{
		{Name: "own", State: "running", Workspace: dir, Instance: "DEV"},
		{Name: "prod-1", State: "running", Workspace: dir, Instance: "PROD"},
		{Name: "prod-2", State: "running", Workspace: dir, Instance: "PROD"},
	}
	if holder, busy := OtherInstanceHolds(boxes, dir, "DEV"); !busy || holder != "prod-1" {
		t.Fatalf("got %q, %v; want prod-1, true", holder, busy)
	}
	if holder, busy := OtherInstanceHolds(nil, dir, "DEV"); busy || holder != "" {
		t.Fatalf("no boxes: got %q, %v", holder, busy)
	}
}
