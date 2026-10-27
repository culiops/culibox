package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
)

func TestAuditIDFloorReadsEveryAuditTable(t *testing.T) {
	dir := t.TempDir()
	if got := auditIDFloor(dir); len(got) != 0 {
		t.Fatalf("no audit.db: floor = %v", got)
	}
	path := filepath.Join(dir, "audit.db")
	b, err := broker.OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Record(broker.Row{Time: time.Unix(100, 0), SessionID: "claude-4", Outcome: broker.OutcomeMinted}); err != nil {
		t.Fatal(err)
	}
	if err := b.RecordApproval(broker.ApprovalRow{Time: time.Unix(100, 0), SessionID: "codex-9", Action: broker.ActionRefused}); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	e, err := egress.OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Record(egress.Row{Time: time.Unix(100, 0), SessionID: "claude-7", Host: "h", Port: 443, Outcome: egress.OutcomeAllowed}); err != nil {
		t.Fatal(err)
	}
	_ = e.Close()

	got := auditIDFloor(dir)
	if len(got) != 2 || got["claude"] != 7 || got["codex"] != 9 {
		t.Fatalf("floor = %v, want claude:7 codex:9", got)
	}
}
