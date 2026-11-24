package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// SSODirectChecker is what spawning an sso_direct binding needs from the
// broker: the read-only gate, run on a fresh fetch of the binding's permission
// set credentials. Kept apart from BrokerHooks so that only the real broker,
// not every fake, has to answer it; hooks that do not implement it cannot vouch
// for an sso_direct binding, and such a spawn is refused.
type SSODirectChecker interface {
	// CheckSSODirect gates the binding with the given key ("" for the legacy
	// inline binding, else a connection name) of bs. The time is when the
	// fetched credentials expire; zero when none were fetched. It never fails
	// the caller: every failure is an Unverified or NotReadOnly result.
	CheckSSODirect(ctx context.Context, bs config.Bindings, key string) (awsprobe.GateResult, time.Time)
}

const (
	// ssoSpawnCheckTimeout bounds the whole gate check of one spawn: the mint
	// (20 s) and the gate (15 s) with a margin.
	ssoSpawnCheckTimeout = 50 * time.Second
	// maxRefusedShown is how many refused items a spawn refusal names; the rest
	// are counted. maxItemLen bounds one item.
	maxRefusedShown = 12
	maxItemLen      = 160
)

// ssoSpawnInfo is what a passed gate left for the session's wire view. It is
// fixed at spawn and persisted with the session.
type ssoSpawnInfo struct {
	Hours     int    `json:"hours,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// ssoKeys are the probe keys of bs whose binding uses the SSO role directly.
func ssoKeys(bs config.Bindings) []string {
	var keys []string
	if bs.AWS != nil {
		if bs.AWS.SSODirect() {
			keys = append(keys, "")
		}
		return keys
	}
	for _, c := range bs.AWSConns {
		if c.SSODirect() {
			keys = append(keys, c.Name)
		}
	}
	return keys
}

// gateSSODirect runs the read-only gate on every sso_direct binding of bs, in
// parallel, and returns a probe result per key (a definite one: the gate
// passed) and the info the wire view needs. Any binding the gate does not call
// read-only refuses the spawn, as does hooks that cannot run the gate. The
// error (ssoGateErr; host: a Host spawn) is shown to the user as is; it names
// refused actions and reasons, never a credential.
func gateSSODirect(hooks BrokerHooks, bs config.Bindings, agent string, host bool) (map[string]awsprobe.Result, map[string]ssoSpawnInfo, error) {
	keys := ssoKeys(bs)
	if len(keys) == 0 {
		return nil, nil, nil
	}
	chk, ok := hooks.(SSODirectChecker)
	if !ok {
		return nil, nil, ssoGateErr(agent, host, "this CuliBox cannot check that an SSO permission set is read-only, so it will not hand one out")
	}
	type outcome struct {
		res awsprobe.GateResult
		exp time.Time
	}
	outs := make([]outcome, len(keys))
	// Detached from any request: a browser that gives up must not turn into a
	// pass, and an error of any kind below is a refusal.
	ctx, cancel := context.WithTimeout(context.Background(), ssoSpawnCheckTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i].res, outs[i].exp = chk.CheckSSODirect(ctx, bs, k)
		}()
	}
	wg.Wait()

	base := make(map[string]awsprobe.Result, len(keys))
	info := make(map[string]ssoSpawnInfo, len(keys))
	var refusals []string
	now := time.Now()
	for i, k := range keys {
		res := outs[i].res
		if res.State != awsprobe.GateReadOnly || res.RoleARN == "" {
			refusals = append(refusals, ssoRefusal(k, res))
			continue
		}
		at := res.CheckedAt
		if at.IsZero() {
			at = now
		}
		base[k] = awsprobe.Result{State: awsprobe.NoneFound, CheckedAt: at}
		info[k] = ssoSpawnInfo{Hours: hoursUntil(outs[i].exp, now), AccountID: awsscope.AccountOf(res.RoleARN)}
	}
	if len(refusals) > 0 {
		sort.Strings(refusals)
		return nil, nil, ssoGateErr(agent, host, strings.Join(refusals, "; "))
	}
	return base, info, nil
}

// ssoGateErr is the gate's refusal: on a box spawn a containment failure
// (box.ErrBoxUnavailable), on a Host spawn the profile's refusal, since the
// user chose Host (#360).
func ssoGateErr(agent string, host bool, why string) error {
	if host {
		return profileRefusal(agent, why)
	}
	return fmt.Errorf("%w: spawn %s: %s", box.ErrBoxUnavailable, agent, why)
}

// ssoRefusal is one binding's refusal as a bounded sentence: every refused
// item as "pattern: why" (past maxRefusedShown the rest are counted), or the
// reason the permission set could not be checked and the IAM reads it lacks.
func ssoRefusal(key string, res awsprobe.GateResult) string {
	who := "the SSO permission set"
	if key != "" {
		who = fmt.Sprintf("connection %q SSO permission set", key)
	}
	var items []string
	for _, v := range res.Refused {
		items = append(items, clip(v.Pattern+": "+v.Why))
	}
	if res.State == awsprobe.GateNotReadOnly {
		s := who + " is not read-only, so its credentials are not handed out"
		return s + ssoItems(items)
	}
	reason := awsprobe.ReasonText(res.Reason)
	if res.Reason == "" {
		reason = "no verdict"
	}
	s := fmt.Sprintf("could not verify that %s is read-only (%s), so no credential was issued", who, reason)
	if len(res.MissingPerms) > 0 {
		s += "; grant it " + clip(strings.Join(res.MissingPerms, ", "))
	}
	return s + ssoItems(items)
}

// ssoItems renders refused items as ": a; b; … and N more".
func ssoItems(items []string) string {
	if len(items) == 0 {
		return ""
	}
	shown := items
	if len(shown) > maxRefusedShown {
		shown = shown[:maxRefusedShown]
	}
	s := ": " + strings.Join(shown, "; ")
	if n := len(items) - len(shown); n > 0 {
		s += fmt.Sprintf("; … and %d more", n)
	}
	return s
}

// clip makes s one line of at most maxItemLen bytes.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxItemLen {
		s = s[:maxItemLen-1] + "…"
	}
	return s
}

// hoursUntil is the whole hours from now to exp, rounded up; 0 when exp is
// unset or past.
func hoursUntil(exp, now time.Time) int {
	if exp.IsZero() || !exp.After(now) {
		return 0
	}
	return int((exp.Sub(now) + time.Hour - 1) / time.Hour)
}
