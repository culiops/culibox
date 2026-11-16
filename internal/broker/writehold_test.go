package broker

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// #325: WriteHold is what the box layer asks before letting another box into
// an exclusive session's folder. It holds from before the grant's claim check
// (so a spawn racing the grant is refused, or is seen by the check) through the
// window and any write credentials still valid after a drop.
func TestWriteHoldFollowsTheWindow(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("no window: %q", why)
	}
	var atClaim string
	b.SetClaimCheck(func(sid string) error { atClaim = b.WriteHold(sid, time.Time{}); return nil })
	if _, err := b.Elevate("aws-1", cookie(60)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(atClaim, "opening a write window") {
		t.Fatalf("the hold must be visible when the claim check runs: %q", atClaim)
	}
	until := now.Add(time.Hour).Local().Format("15:04")
	if why := b.WriteHold("aws-1", time.Time{}); !strings.Contains(why, "write window open until "+until) {
		t.Fatalf("open window: %q", why)
	}
	// A write credential handed out, then a drop: it stays valid for a while.
	b.mu.Lock()
	b.noteElevatedExp("aws-1", now.Add(10*time.Minute))
	b.mu.Unlock()
	b.Drop("aws-1", CredCookie)
	// The STS expiry is AWS's clock: the hold runs a minute past it.
	last := now.Add(11 * time.Minute).Local().Format("15:04")
	if why := b.WriteHold("aws-1", time.Time{}); !strings.Contains(why, "stay valid until "+last) {
		t.Fatalf("after a drop: %q", why)
	}
	*now = now.Add(10 * time.Minute)
	b.Elevation("aws-1") // the panel's poll forgets an expired leftover; not inside the margin
	if why := b.WriteHold("aws-1", time.Time{}); why == "" {
		t.Fatal("at the credentials' expiry the clock-skew minute still holds the folder")
	}
	*now = now.Add(time.Minute - time.Second)
	b.Elevation("aws-1")
	if why := b.WriteHold("aws-1", time.Time{}); why == "" {
		t.Fatal("inside the clock-skew minute the folder is still held")
	}
	*now = now.Add(time.Second)
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("the dropped window's credentials have expired, margin included: %q", why)
	}
	b.Elevation("aws-1")
	b.mu.Lock()
	left := len(b.dropped)
	b.mu.Unlock()
	if left != 0 {
		t.Fatal("the leftover is forgotten once the margin has passed")
	}
	// A new window, then its timer.
	if _, err := b.Elevate("aws-1", cookie(30)); err != nil {
		t.Fatal(err)
	}
	(*ts)[len(*ts)-1].f()
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("after expiry: %q", why)
	}
	// The session ends: the broker forgets it.
	if _, err := b.Elevate("aws-1", cookie(30)); err != nil {
		t.Fatal(err)
	}
	b.Forget("aws-1")
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("after Forget: %q", why)
	}
}

// A grant the claim check refuses leaves no hold behind, so spawns work again.
func TestRefusedGrantReleasesTheHold(t *testing.T) {
	b, _, _, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.SetClaimCheck(func(string) error {
		return errors.New(`session "sh:proj" (sh-1) uses this folder from another box; end it first`)
	})
	if _, err := b.Elevate("aws-1", cookie(60)); !errors.Is(err, ErrElevateRefused) || !strings.Contains(err.Error(), `session "sh:proj"`) {
		t.Fatalf("got %v", err)
	}
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("a refused grant left a hold: %q", why)
	}
}

// A grant refused before the claim check never holds the folder either, and
// a failed audit write withdraws the window and its hold.
func TestHoldIsReleasedOnEveryRefusal(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.SetClaimCheck(func(sid string) error { return nil })
	if _, err := b.Elevate("aws-1", cookie(45)); !errors.Is(err, ErrElevateRefused) {
		t.Fatalf("got %v", err)
	}
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("a refused grant left a hold: %q", why)
	}
	rec.apprErr = errors.New("disk full")
	if _, err := b.Elevate("aws-1", cookie(30)); !errors.Is(err, ErrElevateRefused) {
		t.Fatalf("got %v", err)
	}
	if why := b.WriteHold("aws-1", time.Time{}); why != "" {
		t.Fatalf("an unaudited grant left a hold: %q", why)
	}
}

// After a restart the broker knows of no window, but a credential issued just
// before it may still be valid: a box adopted less than a credential's life
// ago is held until then.
func TestWriteHoldCoversCredentialsFromBeforeARestart(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	adopted := *now
	until := adopted.Add(minElevatedRemaining).Local().Format("15:04")
	if why := b.WriteHold("aws-1", adopted); !strings.Contains(why, "restarted") || !strings.Contains(why, until) {
		t.Fatalf("just adopted: %q", why)
	}
	*now = adopted.Add(minElevatedRemaining)
	if why := b.WriteHold("aws-1", adopted); why != "" {
		t.Fatalf("every credential from before the restart has expired: %q", why)
	}
}
