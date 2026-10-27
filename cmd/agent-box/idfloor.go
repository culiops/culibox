package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
	"github.com/culiops/agent-box/internal/session"
)

// auditIDFloor is the highest session number per agent that any audit row
// carries. It backs up session-ids.json: if that file is lost, a new session
// still never takes an id whose credential, approval or egress history is on
// disk. It reads through short-lived handles, so it works whether or not the
// broker or egress services started, and never creates audit.db itself.
func auditIDFloor(dataDir string) map[string]int {
	path := filepath.Join(dataDir, "audit.db")
	if _, err := os.Stat(path); err != nil {
		return map[string]int{}
	}
	var ids []string
	if a, err := broker.OpenAudit(path); err != nil {
		log.Printf("session id floor: credential audit: %v", err)
	} else {
		if got, err := a.SessionIDs(); err != nil {
			log.Printf("session id floor: credential audit: %v", err)
		} else {
			ids = append(ids, got...)
		}
		_ = a.Close()
	}
	if a, err := egress.OpenAudit(path); err != nil {
		log.Printf("session id floor: egress audit: %v", err)
	} else {
		if got, err := a.SessionIDs(); err != nil {
			log.Printf("session id floor: egress audit: %v", err)
		} else {
			ids = append(ids, got...)
		}
		_ = a.Close()
	}
	return session.FloorFromIDs(ids)
}
