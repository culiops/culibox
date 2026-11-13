//go:build upgrade

package upgradetest

import (
	"reflect"
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
