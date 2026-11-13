package broker

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubRefusesAfterRevokeAll(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	if w := ghCall(b, tokA("s"), ghBody); w.Code != 200 {
		t.Fatalf("before: %d", w.Code)
	}
	b.RevokeAllGitHub(context.Background())
	rowsBefore := len(rec.rows)
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "CuliBox is shutting down") {
		t.Fatalf("after: %d %s", w.Code, w.Body)
	}
	if fg.callCount() != 1 || len(rec.rows) != rowsBefore || len(b.ghIssued) != 0 || len(b.ghCache) != 0 {
		t.Fatalf("minted after RevokeAll: calls %d rows %d", fg.callCount(), len(rec.rows))
	}
}

func TestRevokeAllWaitsForEarlierRevokes(t *testing.T) {
	b, fg, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	ghCall(b, tokA("s"), ghBody)
	var finished atomic.Bool
	fg.revokeFn = func(context.Context, string) error {
		time.Sleep(60 * time.Millisecond)
		finished.Store(true)
		return nil
	}
	b.Forget("s") // a revoke is now in flight
	b.RevokeAllGitHub(context.Background())
	if !finished.Load() {
		t.Fatal("RevokeAllGitHub returned while an earlier revoke was still running")
	}
}

func TestRevokeAllWaitIsBoundedByCtx(t *testing.T) {
	b, fg, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	ghCall(b, tokA("s"), ghBody)
	fg.revokeFn = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	b.ghRevokeTimeout = 2 * time.Second // the Forget-started revoke outlives the caller's ctx
	b.ghTokenTimeout = 2 * time.Second
	b.Forget("s")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	b.RevokeAllGitHub(ctx)
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("RevokeAllGitHub took %v, ctx was 50ms", d)
	}
	b.revokeWG.Wait()
}

func TestRevokeSkipsExpired(t *testing.T) {
	for _, all := range []bool{false, true} {
		b, fg, rec, now := ghSetup(t, map[string]SessionView{"s": ghLive()})
		ghCall(b, tokA("s"), ghBody)
		*now = now.Add(2 * time.Hour) // past the hour the token lives
		if all {
			b.RevokeAllGitHub(context.Background())
		} else {
			b.RevokeGitHub("s")
		}
		b.revokeWG.Wait()
		if got := fg.revokedList(); len(got) != 0 {
			t.Fatalf("all=%v: an expired token was sent to GitHub: %v", all, got)
		}
		if rows := revokeRows(rec); len(rows) != 0 {
			t.Fatalf("all=%v: rows for an expired token: %+v", all, rows)
		}
		if len(b.ghIssued) != 0 {
			t.Fatalf("all=%v: still listed: %v", all, b.ghIssued)
		}
	}
}

func TestRevokeOneSlowTokenDoesNotStarveTheRest(t *testing.T) {
	b, fg, rec, now := ghSetup(t, map[string]SessionView{"s": ghLive()})
	toks := mintN(t, b, now, "s", 2)
	fg.revokeFn = func(ctx context.Context, tok string) error {
		if tok == toks[0] {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	b.ghTokenTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	b.RevokeAllGitHub(ctx)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	rows := revokeRows(rec)
	if len(rows) != 2 || rows[0].Outcome != OutcomeRevokeFailed || rows[1].Outcome != OutcomeRevoked {
		t.Fatalf("rows: %+v", rows)
	}
	if got := fg.revokedList(); len(got) != 2 {
		t.Fatalf("second token never attempted: %v", got)
	}
}
