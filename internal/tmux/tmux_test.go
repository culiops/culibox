package tmux

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestList_Parse(t *testing.T) {
	c := &CLIClient{Run: func(args ...string) ([]byte, error) {
		return []byte("work\t1784860775\nplay\t1784860800\n"), nil
	}}
	got, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []Session{
		{Name: "work", Created: time.Unix(1784860775, 0)},
		{Name: "play", Created: time.Unix(1784860800, 0)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestList_NoServerIsEmpty(t *testing.T) {
	c := &CLIClient{Run: func(args ...string) ([]byte, error) {
		return []byte("no server running on /tmp/tmux-1000/default"), errors.New("exit status 1")
	}}
	got, err := c.List()
	if err != nil {
		t.Fatalf("no-server must be empty, not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestNew_ComposesArgv(t *testing.T) {
	var gotArgs []string
	c := &CLIClient{Run: func(args ...string) ([]byte, error) { gotArgs = args; return nil, nil }}
	if err := c.New("sess", "/work", []string{"claude", "--resume"}, []string{"FOO=bar"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"new-session", "-d", "-s", "sess", "-c", "/work", "-e", "FOO=bar", "--", "claude", "--resume"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("got %v want %v", gotArgs, want)
	}
}

func TestHas_MissingIsFalse(t *testing.T) {
	c := &CLIClient{Run: func(args ...string) ([]byte, error) {
		return []byte("can't find session: x"), errors.New("exit status 1")
	}}
	ok, err := c.Has("x")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("missing session should be false")
	}
}
