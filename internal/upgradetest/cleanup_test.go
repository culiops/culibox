//go:build upgrade

package upgradetest

import (
	"reflect"
	"strings"
	"testing"
)

func TestKeychainCleanupArgs(t *testing.T) {
	got := keychainCleanupArgs([]string{"a", "b", "a", ""})
	want := [][]string{
		{"delete-generic-password", "-s", "agent-box-store", "-a", "a"},
		{"delete-generic-password", "-s", "agent-box-store", "-a", "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDarwinSocketDirs(t *testing.T) {
	got := darwinSocketDirs([]string{"/tmp/abupX/h/r", "/tmp/abupX/h/r", ""}, 501)
	if len(got) != 1 || !strings.HasPrefix(got[0], "/tmp/agent-box-501-") {
		t.Fatalf("got %v", got)
	}
}
