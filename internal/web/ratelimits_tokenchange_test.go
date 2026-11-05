package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/session"
)

// What a token change must do to an account's quota readings, in the order it
// really happens: the OLD token's snapshot is on disk and keeps being polled
// after the change, until a session on the NEW token replies and writes its own.

type tokenFlow struct {
	t     *testing.T
	s     *Server      // accounts + polling
	a     *tokenRoutes // store value routes, sharing the account registry
	clock *time.Time
	file  string // the account's snapshot file
	box   []session.BoxFile
}

var tokenT0 = time.Unix(1_800_000_000, 0)

func (f *tokenFlow) at(d time.Duration) { *f.clock = tokenT0.Add(d) }

func (f *tokenFlow) weekA() int64 { return tokenT0.Add(6 * 24 * time.Hour).Unix() }
func (f *tokenFlow) weekB() int64 { return tokenT0.Add(3 * 24 * time.Hour).Unix() }
func (f *tokenFlow) five() int64  { return tokenT0.Add(2 * time.Hour).Unix() }

func (f *tokenFlow) writeFile(updated int64, week float64, weekReset int64) {
	f.t.Helper()
	os.MkdirAll(filepath.Dir(f.file), 0o700)
	if err := os.WriteFile(f.file, rlBody(updated, 9, f.five(), week, weekReset), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *tokenFlow) poll() (RateLimits, bool) {
	f.s.BoxFiles = boxFilesOf(f.box...)
	rl, ok := f.s.rateLimitsByAccount()["work"]
	return rl, ok
}

// newTokenFlow: an account "work" linked to its own token id, a clock the test
// moves, and the old token's snapshot (opus-like 76% and haiku-like 94%) polled
// and remembered, one from the file and one from a box.
func newTokenFlow(t *testing.T) *tokenFlow {
	t.Helper()
	s, _, _ := acctServer(t)
	a := newTokenRoutes(t)
	a.srv.Accounts = s.Accounts
	clock := tokenT0
	old := rateLimitsClock
	rateLimitsClock = func() time.Time { return clock }
	t.Cleanup(func() { rateLimitsClock = old })
	f := &tokenFlow{t: t, s: s, a: a, clock: &clock}

	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`); rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	for _, r := range s.Accounts.List() {
		if r.Name == "work" {
			f.file = filepath.Join(r.ConfigDir, account.RateLimitsName)
		}
	}
	if code, _, raw := a.call("PUT", pathValues+account.TokenID("work"), `{"value":"old-token-value"}`); code != 204 {
		t.Fatalf("store value %d %s", code, raw)
	}
	if rec := doJSON(t, s.handleAccount, "PUT", "/api/accounts/work", `{"token_secret":"`+account.TokenID("work")+`","inherit_personal":false}`); rec.Code != 200 {
		t.Fatalf("link %d %s", rec.Code, rec.Body)
	}

	f.at(100 * time.Second)
	f.box = []session.BoxFile{{Box: "b1", Account: "work", Data: rlBody(tokenT0.Unix()+100, 9, f.five(), 76, f.weekA())}}
	f.writeFile(tokenT0.Unix()+100, 94, f.weekB())
	rl, ok := f.poll()
	if got := seenWeek(rl); !ok || got[f.weekA()] != 76 || got[f.weekB()] != 94 {
		t.Fatalf("setup: seen = %v ok=%v", got, ok)
	}
	return f
}

// afterChange is the rest of the scenario, once the token has changed at +200s.
func (f *tokenFlow) afterChange() {
	f.t.Helper()
	// The old file is gone (so a restart cannot bring its buckets back)...
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		f.t.Fatalf("the old token's snapshot file was left on disk: %v", err)
	}
	// ...but the old token's readings keep arriving: the file again (a writer
	// that was mid-flight), and the retained box's file.
	f.at(210 * time.Second)
	f.writeFile(tokenT0.Unix()+150, 94, f.weekB())
	if rl, ok := f.poll(); ok {
		f.t.Fatalf("an old reading was shown after the token change: week %v seen %v", rl.SevenDay, seenWeek(rl))
	}
	// The new token's first session replies and writes its own snapshot.
	f.at(300 * time.Second)
	f.writeFile(tokenT0.Unix()+300, 3, f.weekA())
	rl, ok := f.poll()
	if !ok || rl.SevenDay == nil || rl.SevenDay.UsedPercentage != 3 {
		f.t.Fatalf("the new token's reading is not shown: %+v ok=%v", rl.SevenDay, ok)
	}
	if got := seenWeek(rl); len(got) != 1 || got[f.weekA()] != 3 {
		f.t.Fatalf("seen = %v, want only the new reading {%d:3}", got, f.weekA())
	}
}

func TestTokenChange_accountPutForgetsTheOldTokensQuota(t *testing.T) {
	f := newTokenFlow(t)
	f.at(200 * time.Second)
	if rec := doJSON(t, f.s.handleAccount, "PUT", "/api/accounts/work", `{"token_secret":"NEW_TOKEN","inherit_personal":false}`); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	f.afterChange()
}

// The normal "Replace token" flow: the account already uses its own token id, so
// only the stored VALUE is replaced and the account itself is never PUT.
func TestTokenChange_replacingTheStoredValueForgetsTheOldTokensQuota(t *testing.T) {
	f := newTokenFlow(t)
	f.at(200 * time.Second)
	if code, _, raw := f.a.call("PUT", pathValues+account.TokenID("work"), `{"value":"new-token-value"}`); code != 204 {
		t.Fatalf("store value %d %s", code, raw)
	}
	f.afterChange()
}

func TestTokenChange_removingTheStoredValueForgetsTheOldTokensQuota(t *testing.T) {
	f := newTokenFlow(t)
	f.at(200 * time.Second)
	if code, _, raw := f.a.call("DELETE", pathValues+account.TokenID("work"), ""); code != 204 {
		t.Fatalf("store value delete %d %s", code, raw)
	}
	f.afterChange()
}

// Saving the account without changing its token is not a token change.
func TestTokenChange_unrelatedSavesKeepTheRememberedQuota(t *testing.T) {
	f := newTokenFlow(t)
	f.at(200 * time.Second)
	// toggling inherit_personal: same token_secret
	if rec := doJSON(t, f.s.handleAccount, "PUT", "/api/accounts/work", `{"token_secret":"`+account.TokenID("work")+`","inherit_personal":true}`); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	// a stored value that is no account's token
	if code, _, raw := f.a.call("PUT", pathValues+"SOME_OTHER_VALUE", `{"value":"v"}`); code != 204 {
		t.Fatalf("store value %d %s", code, raw)
	}
	if _, err := os.Stat(f.file); err != nil {
		t.Fatalf("the snapshot file was removed: %v", err)
	}
	f.at(210 * time.Second)
	rl, ok := f.poll()
	if got := seenWeek(rl); !ok || got[f.weekA()] != 76 || got[f.weekB()] != 94 {
		t.Fatalf("remembered readings were dropped by an unrelated save: %v ok=%v", got, ok)
	}
}

// The statusLine script copies the other buckets of the previous file into each
// new write. After a token change the first write of the new token can therefore
// carry the old token's buckets, stamped with their own (old) updated_at.
func TestTokenChange_bucketsCopiedFromTheOldFileAreIgnored(t *testing.T) {
	f := newTokenFlow(t)
	f.at(200 * time.Second)
	if rec := doJSON(t, f.s.handleAccount, "PUT", "/api/accounts/work", `{"token_secret":"NEW_TOKEN","inherit_personal":false}`); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	f.at(300 * time.Second)
	os.MkdirAll(filepath.Dir(f.file), 0o700)
	body := fmt.Sprintf(`{"updated_at":%d,"five_hour":{"used_percentage":1,"resets_at":%d},"seven_day":{"used_percentage":3,"resets_at":%d},
	  "by_model":{"opus":{"updated_at":%d,"five_hour":{"used_percentage":1,"resets_at":%d},"seven_day":{"used_percentage":3,"resets_at":%d}},
	              "haiku":{"updated_at":%d,"seven_day":{"used_percentage":94,"resets_at":%d}}}}`,
		tokenT0.Unix()+300, f.five(), f.weekA(), tokenT0.Unix()+300, f.five(), f.weekA(), tokenT0.Unix()+100, f.weekB())
	os.WriteFile(f.file, []byte(body), 0o600)
	f.box = nil
	rl, ok := f.poll()
	if !ok || rl.SevenDay.UsedPercentage != 3 {
		t.Fatalf("new reading not shown: %+v ok=%v", rl.SevenDay, ok)
	}
	if _, have := rl.ByModel["haiku"]; have {
		t.Fatalf("a bucket copied from the old token's file was served: %v", rl.ByModel)
	}
	if got := seenWeek(rl); len(got) != 1 || got[f.weekA()] != 3 {
		t.Fatalf("seen = %v, want only the new reading", got)
	}
}
