package broker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// signalReader reports its first Read, then serves the body once release closes.
type signalReader struct {
	first   chan struct{}
	release chan struct{}
	once    sync.Once
	body    io.Reader
}

func (r *signalReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.first) })
	<-r.release
	return r.body.Read(p)
}

// The box controls when its request body arrives. A request that has passed
// auth but is still waiting for its body when RevokeAllGitHub runs must mint
// nothing: the closing check belongs in the section that registers the flight.
func TestGitHubBodyStalledAcrossRevokeAllMintsNothing(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	body := &signalReader{first: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader(ghBody)}
	r := httptest.NewRequest(http.MethodPost, "/v1/github/token", body)
	r.Header.Set("Authorization", "Bearer "+tokA("s"))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { b.Handler().ServeHTTP(w, r); close(done) }()
	<-body.first // past auth and the nil-minter check, blocked reading the body
	b.RevokeAllGitHub(context.Background())
	close(body.release)
	<-done
	b.revokeWG.Wait()
	if w.Code != 503 || strings.Contains(w.Body.String(), "ghs_") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if fg.callCount() != 0 || len(b.ghIssued) != 0 || len(b.ghCache) != 0 || len(b.ghFlight) != 0 || len(rec.rows) != 0 {
		t.Fatalf("minted after shutdown: calls %d issued %v rows %+v", fg.callCount(), b.ghIssued, rec.rows)
	}
}

// After RevokeAllGitHub, revokeAsync must not add to revokeWG (an Add racing
// RevokeAllGitHub's Wait is a WaitGroup misuse): it revokes in the caller.
func TestRevokeAsyncAfterRevokeAllRunsInline(t *testing.T) {
	b, fg, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	b.RevokeAllGitHub(context.Background())
	var ran atomic.Bool
	fg.revokeFn = func(context.Context, string) error { time.Sleep(30 * time.Millisecond); ran.Store(true); return nil }
	b.revokeAsync("s", []issued{{value: "ghs_LATE", owner: "culiops", installation: 1}})
	if !ran.Load() {
		t.Fatal("revokeAsync after RevokeAllGitHub returned before the revoke ran")
	}
}

// Drives Add and Wait toward each other under -race: late revokes are issued
// while RevokeAllGitHub is waiting. It cannot force the bad interleaving (that
// depends on scheduler timing), so it is a stress guard; the deterministic
// proof is the ordering argument in revokeAsync's comment plus the test above.
func TestRevokeAsyncRacingRevokeAllWait(t *testing.T) {
	for range 100 {
		b, fg, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
		fg.revokeFn = func(context.Context, string) error { time.Sleep(time.Millisecond); return nil }
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 5 {
					b.revokeAsync("s", []issued{{value: "ghs_X", owner: "culiops", installation: 1}})
				}
			}()
		}
		b.RevokeAllGitHub(context.Background())
		wg.Wait()
		b.revokeWG.Wait()
	}
}
