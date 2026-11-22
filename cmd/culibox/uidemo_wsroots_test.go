//go:build uidemo

package main

import (
	"reflect"
	"testing"
)

func TestUIDemoSplitBases(t *testing.T) {
	got := uidemoSplitBases("/a/b/:  /c : :/d//e")
	if want := []string{"/a/b", "/c", "/d/e"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
