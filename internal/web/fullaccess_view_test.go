package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
)

func ackedConn(name string) config.AWSConnection {
	c := config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{SourceProfile: "p", Region: "us-east-1", Mode: config.AWSModeSSODirect}}
	a := awsprobe.NewAck(wideGate("iam:*", "s3:putobject"), "web", time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC))
	c.FullAccessAck = &a
	return c
}

func TestViewFullAccessFieldsComeFromTheAck(t *testing.T) {
	plain := config.AWSConnection{Name: "plain", AWSBinding: config.AWSBinding{SourceProfile: "p", Region: "us-east-1", Mode: config.AWSModeSSODirect}}
	ar := awsConn("c", "p", arnRead)
	a := newAWSRoutes(t)
	a.svc.gate = wideGate("iam:*", "s3:putobject")
	if code, m, _ := a.call("POST", pathAWS, ackBody(ssoConnJSON("wide"), `["iam:*","s3:putobject"]`)); code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	a.svc.gate = readOnlyGate()
	if code, _, _ := a.call("POST", pathAWS, ssoConnJSON("plain")); code != 201 {
		t.Fatal(code)
	}
	_ = plain
	_ = ar
	raw := map[string]map[string]any{}
	for _, e := range a.srv.credView().AWS {
		b, _ := json.Marshal(e)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		raw[e.Name] = m
	}
	w := raw["wide"]
	ack, _ := w["full_access_ack"].(map[string]any)
	if w["full_access"] != true || ack == nil || ack["state"] != "not_read_only" || ack["refused_count"] != float64(2) || ack["actor"] != "web" || ack["at"] == nil {
		t.Fatalf("wide: %v", w)
	}
	if _, has := ack["refused"]; has {
		t.Fatalf("the full refused list is on the wire: %v", ack)
	}
	if strings.Contains(mustJSON(t, w), "caller") || strings.Contains(mustJSON(t, w), "iam:*") {
		t.Fatalf("pattern or caller leaked: %v", w)
	}
	if p := raw["plain"]; p["full_access"] == true || p["full_access_ack"] != nil {
		t.Fatalf("plain: %v", p)
	}
}

func TestFullAccessFalseForAssumeRole(t *testing.T) {
	c := awsConn("c", "p", arnRead)
	if c.FullAccess() {
		t.Fatal("assume_role is never full access")
	}
	fx := newCredFixture(t, config.Config{}, nil)
	fx.pm.SetAWSConnections([]config.AWSConnection{c, ackedConn("wide")})
	got := map[string]bool{}
	for _, m := range fx.pm.AWSConnectionMetaList() {
		got[m.Name] = m.FullAccess
	}
	if got["c"] || !got["wide"] {
		t.Fatalf("%v", got)
	}
}
