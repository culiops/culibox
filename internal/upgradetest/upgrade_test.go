//go:build upgrade

package upgradetest

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/secretstore"
)

// needBins returns the vN-1 and vN binaries scripts/upgrade-test.sh built, or
// skips: go test -tags upgrade alone runs only the unit tests.
func needBins(t *testing.T) (prev, cur string) {
	prev, cur = os.Getenv("AB_UPGRADE_PREV_BIN"), os.Getenv("AB_UPGRADE_CUR_BIN")
	if prev == "" || cur == "" {
		t.Skip("SKIP needs AB_UPGRADE_PREV_BIN/AB_UPGRADE_CUR_BIN (run scripts/upgrade-test.sh)")
	}
	return prev, cur
}

// scenario is one throwaway HOME with its data root, port and fakes.
type scenario struct {
	tmp, home, fake, port, log string
	env                        Env
}

func newScenario(t *testing.T) *scenario {
	t.Helper()
	tmp, err := os.MkdirTemp("/tmp", "abup")
	if err != nil {
		t.Fatal(err)
	}
	s := &scenario{tmp: tmp, home: filepath.Join(tmp, "h"), fake: filepath.Join(tmp, "fake"), port: FreePort(), log: filepath.Join(tmp, "serve.log")}
	t.Cleanup(func() {
		if os.Getenv("AB_UPGRADE_KEEP") == "1" {
			t.Logf("kept %s (serve log: %s)", tmp, s.log)
			return
		}
		if t.Failed() {
			t.Logf("serve log tail:\n%s", LogTail(s.log, 80))
		}
		_ = os.RemoveAll(tmp)
	})
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	WriteFakes(t, s.fake)
	return s
}

func phase(t *testing.T, name string, start time.Time) {
	t.Logf("PHASE %s %v", name, time.Since(start).Round(10*time.Millisecond))
}

func TestUpgrade(t *testing.T) {
	prev, cur := needBins(t)
	t.Logf("upgrade %s -> %s", os.Getenv("AB_UPGRADE_PREV_VERSION"), os.Getenv("AB_UPGRADE_CUR_VERSION"))
	t.Run("S1", func(t *testing.T) { testPinnedRoot(t, prev, cur) })
	t.Run("S2", func(t *testing.T) { testDefaultRoot(t, prev, cur) })
}

// testPinnedRoot is S1: the data root pinned with AGENT_BOX_HOME, a strict
// byte compare, and one live session across the upgrade.
func testPinnedRoot(t *testing.T, prev, cur string) {
	s := newScenario(t)
	root := filepath.Join(s.home, "r")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { KillSessions(root) })
	cleanupStoreKeys(t, func() []string { return []string{secretstore.KeyHash(root)} })
	s.env = Env{Home: s.home, RootEnv: root}
	WriteConfig(t, root, s.port, s.fake)
	f := &Fixture{Root: root, Home: s.home, Fake: s.fake, Log: s.log}

	start := time.Now()
	c := NewClient(s.port)
	old := Start(t, prev, s.port, s.env, s.log)
	Populate(t, c, prev, s.env, f, true)
	old.Stop(t)
	SeedOffline(t, root, s.tmp)
	f.AuditDB = auditDBRows(t, filepath.Join(root, "audit.db"))
	before, err := Take(s.home)
	if err != nil {
		t.Fatal(err)
	}
	phase(t, "S1 populate (vN-1)", start)

	start = time.Now()
	srv := Start(t, cur, s.port, s.env, s.log)
	observeLiveSession(t, c, f)
	CheckUpgraded(t, c, f, nil)
	srv.Stop(t)
	after, err := Take(s.home)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range Diff(before, after, DiffOptions{Rules: AllowRules("r", nil)}) {
		t.Errorf("unexpected change: %s", v)
	}
	if miss := missingDBRows(f.AuditDB, auditDBRows(t, filepath.Join(root, "audit.db"))); len(miss) > 0 {
		t.Errorf("audit.db rows lost in the upgrade: %v", miss)
	} else {
		t.Logf("PASS every audit.db row vN-1 wrote is still in audit.db")
	}
	ReadStoreCopy(t, root, storeValues)
	phase(t, "S1 upgrade (vN)", start)

	start = time.Now()
	Reverse(t, "S1", c, prev, s.env, f, root)
	phase(t, "S1 rollback (vN-1)", start)
}

// testDefaultRoot is S2: no root env var, so vN-1 uses its default root and vN
// resolves its own, moving ~/.local/share/agent-box to ~/.local/share/culibox
// when vN-1 used the legacy root (#255). No session is left running: a live
// session blocks the move.
func testDefaultRoot(t *testing.T, prev, cur string) {
	s := newScenario(t)
	legacy, newRoot := filepath.Join(s.home, legacyRel), filepath.Join(s.home, newRel)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { KillSessions(legacy); KillSessions(newRoot) })
	// The legacy hash is taken now: after the move the legacy path resolves to
	// the new root. The new root's hash is taken at cleanup, once it exists.
	legacyHash := secretstore.KeyHash(legacy)
	cleanupStoreKeys(t, func() []string { return []string{legacyHash, secretstore.KeyHash(newRoot)} })
	s.env = Env{Home: s.home}
	WriteConfig(t, legacy, s.port, s.fake)

	start := time.Now()
	c := NewClient(s.port)
	old := Start(t, prev, s.port, s.env, s.log)
	// vN-1 serves the root that holds its login token: the legacy one for every
	// release before the move, the culibox one for a release that already moved.
	prevRoot := legacy
	if _, err := os.Stat(filepath.Join(legacy, "login.token")); err != nil {
		prevRoot = newRoot
	}
	f := &Fixture{Root: prevRoot, Home: s.home, Fake: s.fake, Log: s.log}
	Populate(t, c, prev, s.env, f, false)
	old.Stop(t)
	SeedOffline(t, prevRoot, s.tmp)
	f.AuditDB = auditDBRows(t, filepath.Join(prevRoot, "audit.db"))
	before, err := Take(s.home)
	if err != nil {
		t.Fatal(err)
	}
	phase(t, "S2 populate (vN-1)", start)

	start = time.Now()
	srv := Start(t, cur, s.port, s.env, s.log)
	moved := CheckMoved(t, s.home)
	root, opts := prevRoot, DiffOptions{}
	switch {
	case moved:
		t.Logf("PASS moved %s -> %s", legacyRel, newRel)
		root = newRoot
		// The two root entries are not generic: CheckMoved asserts the link, and
		// the new root must keep the legacy root's mode (checked after the stop).
		opts = DiffOptions{Rules: AllowRules(newRel, moveRewrite), MapBefore: MoveMapper(legacyRel, newRel), Ignore: []string{legacyRel, newRel}}
		CheckUpgraded(t, c, f, moveRewrite)
	case prevRoot == newRoot:
		t.Logf("OBSERVE no move: vN-1 already uses the culibox root")
		opts.Rules = AllowRules(newRel, nil)
		CheckUpgraded(t, c, f, nil)
	default:
		t.Errorf("vN did not move the legacy root vN-1 used; serve log:\n%s", rootmoveLines(s.log))
		opts.Rules = AllowRules(legacyRel, nil)
		CheckUpgraded(t, c, f, nil)
	}
	srv.Stop(t)
	after, err := Take(s.home)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range Diff(before, after, opts) {
		t.Errorf("unexpected change: %s", v)
	}
	if moved && before[legacyRel].Mode != after[newRel].Mode {
		t.Errorf("the moved root's mode is %v, the legacy root's was %v", after[newRel].Mode, before[legacyRel].Mode)
	}
	if miss := missingDBRows(f.AuditDB, auditDBRows(t, filepath.Join(root, "audit.db"))); len(miss) > 0 {
		t.Errorf("audit.db rows lost in the upgrade: %v", miss)
	} else {
		t.Logf("PASS every audit.db row vN-1 wrote is still in audit.db")
	}
	ReadStoreCopy(t, root, storeValues)
	phase(t, "S2 upgrade (vN)", start)

	start = time.Now()
	Reverse(t, "S2", c, prev, s.env, f, root)
	phase(t, "S2 rollback (vN-1)", start)
}

// observeLiveSession records what vN does with a session vN-1 left running.
// Live sessions across an upgrade are out of scope (#229): this logs, it does
// not assert.
func observeLiveSession(t *testing.T, c *Client, f *Fixture) {
	t.Helper()
	alive := f.LiveSrvPID > 0 && syscall.Kill(f.LiveSrvPID, 0) == nil
	var list []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Exited bool   `json:"exited"`
	}
	if _, err := c.Do("GET", "/api/sessions", nil, &list); err != nil {
		t.Logf("OBSERVE live session: GET /api/sessions failed: %v", err)
		return
	}
	for _, s := range list {
		if s.ID == f.LiveSession {
			t.Logf("OBSERVE live session %s after the upgrade: listed by vN, status %q, exited %v, abduco server alive %v", s.ID, s.Status, s.Exited, alive)
			return
		}
	}
	t.Logf("OBSERVE live session %s after the upgrade: not listed by vN, abduco server alive %v", f.LiveSession, alive)
}
