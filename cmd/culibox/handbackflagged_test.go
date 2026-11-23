package main

import (
	"testing"
	"time"

	"github.com/culiops/culibox/internal/handback"
)

func TestFlaggedViewCounts(t *testing.T) {
	h := newStubHandback(t, nil)
	v := newFlaggedView(h.store)
	at := time.Unix(100, 0).UTC()
	two := handback.Report{Flags: []handback.Flag{{Path: "a"}, {Path: "b"}}}
	merge := func(key string, base handback.Record, rep handback.Report) handback.Record {
		rec, err := h.store.Merge(key, base, at, rep)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	of := "/home/u/repo"
	merge(handback.CloneKey(of, "x"), handback.Record{Kind: handback.KindClone, CloneOf: of, Branch: "box/x"}, two)
	merge(handback.CloneKey(of, "y"), handback.Record{Kind: handback.KindClone, CloneOf: of, Branch: "box/y"}, handback.Report{Flags: []handback.Flag{{Path: "c"}}})
	ip := merge(handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, two)
	merge(handback.InPlaceKey("/cut"), handback.Record{Kind: handback.KindInPlace, Workspace: "/cut"}, handback.Report{Truncated: "cap"})

	if got := v.Count(of, ""); got != 3 {
		t.Errorf("clone count = %d, want 3 (both branches)", got)
	}
	if got := v.Count("", "/ws"); got != 2 {
		t.Errorf("in-place count = %d, want 2", got)
	}
	if got := v.Count("", "/cut"); got != 0 {
		t.Errorf("a cut-short scan with no flags counts %d, want 0 (a count of flags)", got)
	}
	if got := v.Count("/other", ""); got != 0 || v.Count("", "") != 0 {
		t.Errorf("unrelated asks must count 0")
	}
	// A clone's of never matches an in-place workspace of the same text, and vice versa.
	if got := v.Count("/ws", ""); got != 0 {
		t.Errorf("clone ask matched an in-place record: %d", got)
	}

	// Reviewed through the view drops the cache at once.
	if err := v.Reviewed(ip.ID, ip.Revision); err != nil {
		t.Fatal(err)
	}
	if got := v.Count("", "/ws"); got != 0 {
		t.Errorf("after Reviewed the count is %d, want 0", got)
	}
}

func TestFlaggedViewCachesBetweenAsks(t *testing.T) {
	h := newStubHandback(t, nil)
	v := newFlaggedView(h.store)
	clock := time.Unix(1000, 0)
	v.now = func() time.Time { return clock }
	if _, err := h.store.Merge(handback.InPlaceKey("/ws"), handback.Record{Kind: handback.KindInPlace, Workspace: "/ws"}, clock, handback.Report{Flags: []handback.Flag{{Path: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if v.Count("", "/ws") != 1 {
		t.Fatal("first read")
	}
	// Written behind the cache's back (a scan): not seen until the TTL passes.
	if err := h.store.Reviewed(handback.RecordID(handback.InPlaceKey("/ws")), 1); err != nil {
		t.Fatal(err)
	}
	if v.Count("", "/ws") != 1 {
		t.Error("a second ask inside the TTL must reuse the read")
	}
	clock = clock.Add(flaggedCountTTL)
	if v.Count("", "/ws") != 0 {
		t.Error("after the TTL the store is read again")
	}
}
