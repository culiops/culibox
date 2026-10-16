package broker

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAuditRecordAndReadBack(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	t0 := time.Unix(1_800_000_000, 0)
	for i, o := range []Outcome{OutcomeMinted, OutcomeSSOExpired, OutcomeMinted} {
		if err := a.Record(Row{Time: t0.Add(time.Duration(i) * time.Minute), SessionID: "claude-3", Profile: "c",
			AccountID: "226198813800", RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent",
			SourceIdentity: "agentbox-claude-3", ScopeHash: "abc", Expires: t0.Add(15 * time.Minute), Outcome: o}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Record(Row{Time: t0, SessionID: "other", Outcome: OutcomeMinted}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.ForSession("claude-3", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Outcome != OutcomeMinted || rows[1].Outcome != OutcomeSSOExpired || !rows[0].Time.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("rows: %+v", rows)
	}
}
