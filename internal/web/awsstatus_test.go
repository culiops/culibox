package web

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/session"
)

func awsAdapter(info map[string]session.Info, rows []broker.Row, rowsErr error, gotLimit *int) AWSStatusAdapter {
	return AWSStatusAdapter{
		Get: func(id string) (session.Info, bool) { i, ok := info[id]; return i, ok },
		Stats: func(id string) (broker.Stats, bool) {
			if id == "claude-3" {
				return broker.Stats{Mints: 2, CacheHits: 5, LastMint: time.Unix(1700000000, 0).UTC(), LastError: "boom"}, true
			}
			return broker.Stats{}, false
		},
		Recent: func(id string, limit int) ([]broker.Row, error) {
			if gotLimit != nil {
				*gotLimit = limit
			}
			return rows, rowsErr
		},
	}
}

func TestAWSStatusAdapter(t *testing.T) {
	info := map[string]session.Info{
		"claude-3": {ID: "claude-3", AWS: &session.AWSInfo{Label: "sandbox", AccountID: "123456789012",
			RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent", Prod: true}},
		"claude-4": {ID: "claude-4"},
	}
	var limit int
	rows := []broker.Row{{SessionID: "claude-3", Outcome: broker.OutcomeMinted, Time: time.Unix(1700000000, 0)}}
	ad := awsAdapter(info, rows, nil, &limit)

	st, ok := ad.AWSStatus("claude-3")
	if !ok {
		t.Fatal("bound session reported no status")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Label     string `json:"label"`
		AccountID string `json:"account_id"`
		RoleARN   string `json:"role_arn"`
		Prod      bool   `json:"prod"`
		Stats     struct {
			Mints     int       `json:"mints"`
			CacheHits int       `json:"cache_hits"`
			LastMint  time.Time `json:"last_mint"`
			LastError string    `json:"last_error"`
		} `json:"stats"`
		Recent []broker.Row `json:"recent"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Label != "sandbox" || got.AccountID != "123456789012" || !got.Prod || !strings.HasSuffix(got.RoleARN, "agentbox-agent") {
		t.Fatalf("binding fields %+v", got)
	}
	if got.Stats.Mints != 2 || got.Stats.CacheHits != 5 || got.Stats.LastError != "boom" || got.Stats.LastMint.IsZero() {
		t.Fatalf("stats %+v", got.Stats)
	}
	if len(got.Recent) != 1 || got.Recent[0].Outcome != broker.OutcomeMinted {
		t.Fatalf("recent %+v", got.Recent)
	}
	if limit != 20 {
		t.Fatalf("audit rows read with limit %d, want the fixed 20 (the store treats <=0 as unlimited)", limit)
	}
	for _, banned := range []string{"nonce", "token", "secret", "AccessKey"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(banned)) {
			t.Errorf("status carries %q: %s", banned, raw)
		}
	}
}

func TestAWSStatusAdapterMisses(t *testing.T) {
	info := map[string]session.Info{"claude-4": {ID: "claude-4"}}
	ad := awsAdapter(info, nil, nil, nil)
	if _, ok := ad.AWSStatus("claude-4"); ok {
		t.Error("a session without a binding has an AWS status")
	}
	if _, ok := ad.AWSStatus("gone-1"); ok {
		t.Error("an unknown session has an AWS status")
	}
}

func TestAWSStatusAdapterNoStatsNoRows(t *testing.T) {
	info := map[string]session.Info{"claude-5": {ID: "claude-5", AWS: &session.AWSInfo{Label: "x"}}}
	ad := awsAdapter(info, nil, errors.New("db closed"), nil)
	st, ok := ad.AWSStatus("claude-5")
	if !ok {
		t.Fatal("bound session reported no status")
	}
	raw, _ := json.Marshal(st)
	// A fresh session has never minted, and a failing audit store must not take the status down.
	if !strings.Contains(string(raw), `"recent":[]`) || !strings.Contains(string(raw), `"mints":0`) {
		t.Fatalf("status %s", raw)
	}
}
