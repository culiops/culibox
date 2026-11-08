package session

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// attentionCase is one row of testdata/attention-cases.json: wire JSON, shared
// with the desktop sidebar's JS test, so a row can never mean one thing to the
// server and another to the client.
type attentionCase struct {
	Name string `json:"name"`
	Info Info   `json:"info"`
	Want string `json:"want"`
}

func TestAttentionOf(t *testing.T) {
	raw, err := os.ReadFile("testdata/attention-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	// Unknown fields fail the test: a fixture key must be a real Info json tag.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cases []attentionCase
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases in testdata/attention-cases.json")
	}
	for _, c := range cases {
		if got := attentionOf(c.Info); got != c.Want {
			t.Errorf("%s: attentionOf = %q, want %q", c.Name, got, c.Want)
		}
	}
}

// List stamps attention last, from the activity and need it has already
// settled, and leaves everything the card already shows exactly as it was.
func TestListCarriesAttention(t *testing.T) {
	done := `{"message":{"role":"assistant","content":[{"type":"text","text":"all green"}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", done)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	// A second session, a shell bound to AWS whose sign-in has expired: sso.
	m.sessions["s2"] = &Session{
		ID: "s2", Agent: "aws", Cwd: "/tmp/proj", Bin: "/bin/zsh", Created: time.Now().Add(-time.Hour),
		AWS: testBinding(),
	}
	hooks.setState("s2", time.Time{}, "example-sso")

	byID := map[string]Info{}
	for _, i := range m.List() {
		byID[i.ID] = i
	}

	turn := byID["s1"]
	if turn.Attention != "done" {
		t.Errorf("turn session: attention %q, want done", turn.Attention)
	}
	// The values List gave before attention existed (measured on the parent
	// commit with the same fixture): activity, need, ask, summary and
	// reply_ready (present, and false: the session is not replyable).
	if turn.Activity != "waiting" || turn.Need != "turn" || turn.Ask != "" || turn.Summary != "all green" {
		t.Errorf("turn session: activity=%q need=%q ask=%q summary=%q, want waiting/turn//all green", turn.Activity, turn.Need, turn.Ask, turn.Summary)
	}
	if turn.ReplyReady == nil || *turn.ReplyReady {
		t.Errorf("turn session: reply_ready %v, want present and false", turn.ReplyReady)
	}

	sso := byID["s2"]
	if sso.Attention != "block" {
		t.Errorf("sso session: attention %q, want block", sso.Attention)
	}
	if sso.Activity != "idle" || sso.Need != "sso" || sso.Ask != "" || sso.Summary != "" || sso.ReplyReady == nil || *sso.ReplyReady {
		t.Errorf("sso session: activity=%q need=%q ask=%q summary=%q reply_ready=%v, want idle/sso///false", sso.Activity, sso.Need, sso.Ask, sso.Summary, sso.ReplyReady)
	}

	// On the wire: present for a session that needs a look, absent otherwise.
	b, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"attention":"done"`)) {
		t.Errorf("wire form lacks attention: %s", b)
	}
	if b, _ := json.Marshal(Info{}); bytes.Contains(b, []byte(`"attention"`)) {
		t.Errorf("an empty attention must be omitted: %s", b)
	}
}
