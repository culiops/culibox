package main

import (
	"strings"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/session"
)

// spawnAudit writes the cred_audit row of a Host session that takes GitHub
// and/or AWS from this machine's own login (#375). It satisfies
// session.SpawnAudit; the recorder bounds the detail.
type spawnAudit struct {
	rec interface{ RecordCred(broker.CredEvent) error }
}

func (a spawnAudit) RecordHostOwnLogin(r session.HostOwnLoginRecord) error {
	return a.rec.RecordCred(broker.CredEvent{
		Action: broker.CredHostOwnLogin, Kind: "session", Name: r.Profile, Actor: r.Actor,
		Detail: strings.Join(r.Kinds, ",") + " · " + r.SessionID + " · " + r.Cwd,
	})
}
