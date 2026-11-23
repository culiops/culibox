package main

import (
	"log"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/handback"
)

// flaggedCountTTL is how long the profile list reuses one read of the pending
// records. Building the list asks once per profile; the records are files, so
// they are read once per burst, not once per profile.
const flaggedCountTTL = 2 * time.Second

// flaggedView is the web layer's view of the handback store: the pending
// records for GET /api/handback and the per-profile flag counts for the
// profile list, both from one cached read of the store the hooks write.
// Marking a record reviewed drops the cache, so the count the page fetches
// next never still shows what the user just reviewed.
type flaggedView struct {
	store handback.Store
	now   func() time.Time

	mu    sync.Mutex
	at    time.Time
	recs  []handback.Record
	valid bool
}

func newFlaggedView(store handback.Store) *flaggedView {
	return &flaggedView{store: store, now: time.Now}
}

// Pending is the store's Pending (good records plus an error naming bad
// files), uncached: the list a user is about to review is always current.
func (v *flaggedView) Pending() ([]handback.Record, error) { return v.store.Pending() }

// Reviewed marks a record reviewed (when it is still at the revision the page
// listed) and forgets the cached counts.
func (v *flaggedView) Reviewed(id string, revision int64) error {
	err := v.store.Reviewed(id, revision)
	v.mu.Lock()
	v.valid = false
	v.mu.Unlock()
	return err
}

// Count sums the pending flags of the records of a clone's repo (of set) or of
// an in-place workspace (workspace set). A record holding only a note about a
// cut-short scan counts 0: the count is of flags.
func (v *flaggedView) Count(of, workspace string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.valid || v.now().Sub(v.at) >= flaggedCountTTL {
		recs, err := v.store.Pending()
		if err != nil {
			log.Printf("handback: some records could not be read for the profile counts: %v", err)
		}
		v.recs, v.at, v.valid = recs, v.now(), true
	}
	n := 0
	for _, r := range v.recs {
		switch {
		case of != "" && r.Kind == handback.KindClone && r.CloneOf == of,
			workspace != "" && r.Kind == handback.KindInPlace && r.Workspace == workspace:
			n += len(r.Flags)
		}
	}
	return n
}
