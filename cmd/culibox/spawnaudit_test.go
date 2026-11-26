package main

import (
	"errors"
	"testing"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/session"
)

type captureCred struct {
	got []broker.CredEvent
	err error
}

func (c *captureCred) RecordCred(e broker.CredEvent) error {
	c.got = append(c.got, e)
	return c.err
}

func TestSpawnAuditWritesOneCredRow(t *testing.T) {
	c := &captureCred{}
	err := spawnAudit{rec: c}.RecordHostOwnLogin(session.HostOwnLoginRecord{
		SessionID: "claude-3", Profile: "p", Cwd: "/home/u/repo", Actor: "web", Kinds: []string{"github", "aws"}})
	if err != nil {
		t.Fatal(err)
	}
	want := broker.CredEvent{Action: "host_own_login", Kind: "session", Name: "p", Actor: "web",
		Detail: "github,aws · claude-3 · /home/u/repo"}
	if len(c.got) != 1 || c.got[0] != want {
		t.Fatalf("rows = %+v, want [%+v]", c.got, want)
	}
	boom := errors.New("audit down")
	if err := (spawnAudit{rec: &captureCred{err: boom}}).RecordHostOwnLogin(session.HostOwnLoginRecord{}); err != boom {
		t.Fatalf("err = %v, want the recorder's own", err)
	}
}
