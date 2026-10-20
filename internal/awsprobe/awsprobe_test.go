package awsprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aws/smithy-go"
)

func decisions(def string, over map[string]string) map[string]string {
	m := map[string]string{}
	for _, a := range WriteActions {
		m[a] = def
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}

func TestWriteActionsAreTheSpecList(t *testing.T) {
	want := []string{"s3:PutObject", "s3:DeleteObject", "s3:CreateBucket", "ec2:RunInstances",
		"ec2:TerminateInstances", "iam:CreateUser", "iam:AttachRolePolicy", "lambda:UpdateFunctionCode",
		"rds:DeleteDBInstance", "dynamodb:PutItem", "cloudformation:CreateStack"}
	if len(WriteActions) != 11 || !reflect.DeepEqual(WriteActions, want) {
		t.Fatalf("WriteActions = %v", WriteActions)
	}
}

func TestClassifyAllDenied(t *testing.T) {
	r := Classify(decisions("implicitDeny", map[string]string{"s3:CreateBucket": "explicitDeny"}))
	if r.State != NoneFound || len(r.Actions) != 0 || r.Reason != "" {
		t.Fatalf("got %+v", r)
	}
}

func TestClassifyAnyAllowed(t *testing.T) {
	r := Classify(decisions("implicitDeny", map[string]string{"iam:CreateUser": "allowed", "s3:PutObject": "allowed"}))
	if r.State != Writes || !reflect.DeepEqual(r.Actions, []string{"s3:PutObject", "iam:CreateUser"}) {
		t.Fatalf("got %+v", r)
	}
}

func TestClassifyMissingActionIsUnverified(t *testing.T) {
	m := decisions("implicitDeny", nil)
	delete(m, "dynamodb:PutItem")
	r := Classify(m)
	if r.State != Unverified || r.Reason != ReasonError {
		t.Fatalf("got %+v", r)
	}
}

func TestClassifyIgnoresUnrequestedActions(t *testing.T) {
	r := Classify(decisions("implicitDeny", map[string]string{"s3:*": "allowed", "<img>": "allowed"}))
	if r.State != NoneFound || len(r.Actions) != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestClassifyUnknownDecisionIsUnverified(t *testing.T) {
	r := Classify(decisions("implicitDeny", map[string]string{"s3:PutObject": "maybe"}))
	if r.State != Unverified || r.Reason != ReasonError {
		t.Fatalf("got %+v", r)
	}
	r = Classify(decisions("implicitDeny", map[string]string{"s3:PutObject": "allowed", "iam:CreateUser": "maybe"}))
	if r.State != Writes || !reflect.DeepEqual(r.Actions, []string{"s3:PutObject"}) {
		t.Fatalf("got %+v", r)
	}
}

func TestSimulateReason(t *testing.T) {
	cases := []struct {
		err  error
		want Reason
	}{
		{&smithy.GenericAPIError{Code: "AccessDenied"}, ReasonSimulateDenied},
		{&smithy.GenericAPIError{Code: "AccessDeniedException"}, ReasonSimulateDenied},
		{fmt.Errorf("wrap: %w", &smithy.GenericAPIError{Code: "AccessDenied"}), ReasonSimulateDenied},
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), ReasonTimeout},
		{errors.New("x"), ReasonError},
		{&smithy.GenericAPIError{Code: "Throttling"}, ReasonError},
	}
	for _, c := range cases {
		if got := SimulateReason(c.err); got != c.want {
			t.Errorf("SimulateReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestReasonText(t *testing.T) {
	want := map[Reason]string{
		ReasonSSOExpired: "SSO expired", ReasonAssumeDenied: "assume-role denied",
		ReasonSimulateDenied: "role cannot simulate itself", ReasonTimeout: "timed out",
		ReasonError: "check failed", ReasonNotChecked: "not checked", ReasonProbeDisabled: "probe disabled",
		Reason("arn:aws:iam::1:role/x"): "check failed",
	}
	for r, w := range want {
		if got := ReasonText(r); got != w {
			t.Errorf("ReasonText(%q) = %q, want %q", r, got, w)
		}
	}
}

func TestNormalizeUnknownStateIsNotChecked(t *testing.T) {
	if got := (Result{State: "read_only"}).Normalize(); !reflect.DeepEqual(got, NotChecked()) {
		t.Errorf("unknown state: %+v", got)
	}
	if got := (Result{}).Normalize(); !reflect.DeepEqual(got, NotChecked()) {
		t.Errorf("zero: %+v", got)
	}
	at := time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)
	if got := (Result{State: Unverified, Reason: "weird", CheckedAt: at}).Normalize(); got.State != Unverified || got.Reason != ReasonError || !got.CheckedAt.Equal(at) {
		t.Errorf("weird reason: %+v", got)
	}
	if got := (Result{State: Unverified}).Normalize(); got.Reason != ReasonNotChecked {
		t.Errorf("empty reason: %+v", got)
	}
	got := Result{State: Writes, Actions: []string{"s3:PutObject", "evil"}}.Normalize()
	if got.State != Writes || !reflect.DeepEqual(got.Actions, []string{"s3:PutObject"}) {
		t.Errorf("writes: %+v", got)
	}
	got = Result{State: NoneFound, Actions: []string{"s3:PutObject"}, Reason: ReasonError}.Normalize()
	if got.State != NoneFound || got.Actions != nil || got.Reason != "" {
		t.Errorf("none_found: %+v", got)
	}
	got = Result{State: Writes, Reason: ReasonError}.Normalize()
	if got.Reason != "" {
		t.Errorf("writes keeps reason: %+v", got)
	}
}

func TestDefinite(t *testing.T) {
	if !(Result{State: Writes}).Definite() || !(Result{State: NoneFound}).Definite() || NotChecked().Definite() {
		t.Fatal("Definite wrong")
	}
}

func TestLookupAndNeedsRecheck(t *testing.T) {
	if got := Lookup(nil, ""); !reflect.DeepEqual(got, NotChecked()) {
		t.Fatalf("Lookup(nil) = %+v", got)
	}
	at := time.Now()
	m := map[string]Result{
		"a": {State: Writes, Actions: []string{"s3:PutObject"}, CheckedAt: at},
		"b": Fail(ReasonTimeout, at),
	}
	if got := NeedsRecheck(m, []string{"c", "b", "a"}); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("NeedsRecheck = %v", got)
	}
}

func TestFieldsJSON(t *testing.T) {
	b, _ := json.Marshal(NotChecked().Fields())
	if string(b) != `{"base_access":"unverified","base_access_reason":"not_checked"}` {
		t.Fatalf("got %s", b)
	}
	at := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	b, _ = json.Marshal(Result{State: Writes, Actions: []string{"s3:PutObject"}, CheckedAt: at}.Fields())
	want := `{"base_access":"writes","base_write_actions":["s3:PutObject"],"base_checked_at":"2026-10-20T08:30:00Z"}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
}
