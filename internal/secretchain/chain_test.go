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
