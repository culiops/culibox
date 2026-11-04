package secretchain

import (
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

// acctProbe is a store that answers Check by id (List/Status) and fails the
// test if anything decrypts a value.
type acctProbe struct {
	t      *testing.T
	ids    []string
	locked bool
}

func (p acctProbe) Get(string) ([]byte, error) {
	p.t.Fatal("AccountTokenPresence decrypted a value")
	return nil, nil
}

func (p acctProbe) List() ([]secretstore.Entry, error) {
	out := make([]secretstore.Entry, len(p.ids))
	for i, id := range p.ids {
		out[i] = secretstore.Entry{ID: id}
	}
	return out, nil
}

func (p acctProbe) Status() secretstore.Status {
	return secretstore.Status{Initialized: true, Locked: p.locked}
}

func TestAccountTokenPresence(t *testing.T) {
	own := account.Account{Name: "work", Provider: "claude", TokenSecret: "ACCT_WORK_TOKEN"}
	foreign := account.Account{Name: "work", Provider: "claude", TokenSecret: "GH_KEY"}
	envOnly, _ := New([]config.SecretSource{envSrc}, nil)
	mk := func(ids []string, locked bool) *Chain {
		c, err := New([]config.SecretSource{storeSrc}, acctProbe{t: t, ids: ids, locked: locked})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		name  string
		a     account.Account
		store *Chain
		want  Presence
	}{
		{"own id stored", own, mk([]string{"ACCT_WORK_TOKEN"}, false), Present},
		{"own id stored, locked", own, mk([]string{"ACCT_WORK_TOKEN"}, true), StoredLocked},
		{"own id not stored", own, mk(nil, false), Absent},
		{"foreign id never read from the store", foreign, mk([]string{"GH_KEY"}, false), Absent},
		{"no store wired", own, nil, Absent},
		{"no token", account.Account{Name: "work"}, mk([]string{"ACCT_WORK_TOKEN"}, false), Absent},
	}
	for _, c := range cases {
		got, err := AccountTokenPresence(envOnly, c.store, c.a)
		if err != nil || got != c.want {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	// The secret_sources chain answers first: an env value wins.
	t.Setenv("ACCT_WORK_TOKEN", "from-env")
	if got, err := AccountTokenPresence(envOnly, mk(nil, true), own); err != nil || got != Present {
		t.Fatalf("env first: %v, %v", got, err)
	}
}
