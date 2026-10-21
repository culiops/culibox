package secretchain

import (
	"errors"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
	"github.com/culiops/agent-box/internal/secretstore"
)

type fakeStore struct {
	vals   map[string][]byte
	locked bool
}

func (f fakeStore) Get(id string) ([]byte, error) {
	if f.locked {
		return nil, secretstore.ErrLocked
	}
	v, ok := f.vals[id]
	if !ok {
		return nil, secretstore.ErrNoSuchValue
	}
	return v, nil
}

var (
	storeSrc = config.SecretSource{Type: "store"}
	envSrc   = config.SecretSource{Type: "env"}
)

func TestChainOrder(t *testing.T) {
	t.Setenv("B", "from-env")
	c, err := New([]config.SecretSource{storeSrc, envSrc}, fakeStore{vals: map[string][]byte{"A": []byte("from-store")}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Resolve("A"); err != nil || !ok || v != "from-store" {
		t.Fatalf("A = %q %v %v", v, ok, err)
	}
	if v, ok, err := c.Resolve("B"); err != nil || !ok || v != "from-env" {
		t.Fatalf("B = %q %v %v", v, ok, err)
	}
	// An invalid id never reaches the store (a locked store would error).
	c2, _ := New([]config.SecretSource{storeSrc}, fakeStore{locked: true})
	if v, ok, err := c2.Resolve("bad/name"); err != nil || ok || v != "" {
		t.Fatalf("bad/name = %q %v %v, want not found", v, ok, err)
	}
}

func TestChainLockedDoesNotFallThrough(t *testing.T) {
	t.Setenv("A", "plaintext")
	c, err := New([]config.SecretSource{storeSrc, envSrc}, fakeStore{locked: true})
	if err != nil {
		t.Fatal(err)
	}
	v, ok, err := c.Resolve("A")
	if !errors.Is(err, credref.ErrLocked) || ok || v != "" {
		t.Fatalf("A = %q %v %v, want ErrLocked", v, ok, err)
	}
}

func TestChainEarlierSourceWins(t *testing.T) {
	t.Setenv("A", "plaintext")
	c, err := New([]config.SecretSource{envSrc, storeSrc}, fakeStore{locked: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Resolve("A"); err != nil || !ok || v != "plaintext" {
		t.Fatalf("A = %q %v %v", v, ok, err)
	}
}

func TestChainStoreValueRules(t *testing.T) {
	c, err := New([]config.SecretSource{storeSrc}, fakeStore{vals: map[string][]byte{
		"A": []byte("tok\r\n"), "B": []byte("two\nlines"), "C": []byte("\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Resolve("A"); err != nil || !ok || v != "tok" {
		t.Fatalf("A = %q %v %v", v, ok, err)
	}
	for _, n := range []string{"B", "C"} {
		if _, _, err := c.Resolve(n); err == nil {
			t.Errorf("%s: want an error for a multi-line or empty value", n)
		}
	}
}

func TestChainStoreSourceNeedsStore(t *testing.T) {
	// Refusing at construction beats silently serving every name from the next source.
	if _, err := New([]config.SecretSource{storeSrc, envSrc}, nil); err == nil {
		t.Fatal("a store source with no store must be an error")
	}
	if _, err := New([]config.SecretSource{envSrc}, nil); err != nil {
		t.Fatalf("no store source needs no store: %v", err)
	}
}

// probeStore is a store that can also say which ids it holds and whether it is
// locked, without decrypting anything. Get fails the test: Check must not
// reach for a value when it can ask by id.
type probeStore struct {
	t      *testing.T
	ids    []string
	locked bool
	listEr error
}

func (p probeStore) Get(string) ([]byte, error) {
	p.t.Helper()
	p.t.Fatal("Check decrypted a store value")
	return nil, nil
}
func (p probeStore) List() ([]secretstore.Entry, error) {
	var out []secretstore.Entry
	for _, id := range p.ids {
		out = append(out, secretstore.Entry{ID: id})
	}
	return out, p.listEr
}
func (p probeStore) Status() secretstore.Status {
	return secretstore.Status{Initialized: true, Locked: p.locked}
}

func TestCheckStoreByIDWithoutDecrypting(t *testing.T) {
	c, err := New([]config.SecretSource{storeSrc}, probeStore{t: t, ids: []string{"A"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Check("A"); err != nil || got != Present {
		t.Fatalf("A = %v %v, want Present", got, err)
	}
	if got, err := c.Check("B"); err != nil || got != Absent {
		t.Fatalf("B = %v %v, want Absent", got, err)
	}
	if got, err := c.Check("bad/name"); err != nil || got != Absent {
		t.Fatalf("bad/name = %v %v, want Absent", got, err)
	}
	locked, _ := New([]config.SecretSource{storeSrc}, probeStore{t: t, ids: []string{"A"}, locked: true})
	if got, err := locked.Check("A"); err != nil || got != StoredLocked {
		t.Fatalf("locked A = %v %v, want StoredLocked", got, err)
	}
	// Absent from a locked store is still absent, as Resolve would say (not found).
	if got, err := locked.Check("B"); err != nil || got != Absent {
		t.Fatalf("locked B = %v %v, want Absent", got, err)
	}
	uninit, _ := New([]config.SecretSource{storeSrc}, probeStore{t: t, listEr: secretstore.ErrNotInitialized})
	if got, err := uninit.Check("A"); err != nil || got != Absent {
		t.Fatalf("uninitialised = %v %v, want Absent", got, err)
	}
}

func TestCheckOrderAndOtherSources(t *testing.T) {
	t.Setenv("E", "plaintext")
	c, err := New([]config.SecretSource{envSrc, storeSrc}, probeStore{t: t, locked: true, ids: []string{"S"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Check("E"); got != Present {
		t.Fatalf("E = %v, want Present", got)
	}
	if got, _ := c.Check("S"); got != StoredLocked {
		t.Fatalf("S = %v, want StoredLocked", got)
	}
	if got, _ := c.Check("NOPE"); got != Absent {
		t.Fatalf("NOPE = %v, want Absent", got)
	}
}

func TestCheckFallsBackToGetAndZeroes(t *testing.T) {
	// A store with no List/Status: Check reads through Get (no other way) and
	// reports only presence. A locked store is StoredLocked.
	c, _ := New([]config.SecretSource{storeSrc}, fakeStore{vals: map[string][]byte{"A": []byte("v")}})
	if got, err := c.Check("A"); err != nil || got != Present {
		t.Fatalf("A = %v %v", got, err)
	}
	if got, _ := c.Check("B"); got != Absent {
		t.Fatalf("B = %v", got)
	}
	l, _ := New([]config.SecretSource{storeSrc}, fakeStore{locked: true})
	if got, _ := l.Check("A"); got != StoredLocked {
		t.Fatalf("locked = %v", got)
	}
}
