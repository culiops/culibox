package clone

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReturnStateRoundTrip(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	at := time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	in := ReturnState{
		At:   at,
		Base: shaN(1),
		NotHome: []NotHome{
			{Branch: "feat/a", Reason: ReasonNotFF, Detail: "host box/feat/a has commits"},
			{Branch: "b", Reason: ReasonOff},
		},
		Error: "could not list branches",
	}
	if err := WriteReturnState(dd, dir, in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadReturnState(dd, dir)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.V != 1 || !got.At.Equal(at) || got.Base != in.Base || got.Error != in.Error {
		t.Fatalf("got %+v", got)
	}
	if len(got.NotHome) != 2 || got.NotHome[0] != in.NotHome[0] || got.NotHome[1] != in.NotHome[1] {
		t.Fatalf("entries %+v", got.NotHome)
	}
	h, _ := cloneKeyHash(dir)
	fi, err := os.Stat(filepath.Join(dd, returnStateDir, h+".json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi, err)
	}
	di, _ := os.Stat(filepath.Join(dd, returnStateDir))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
}

func TestReturnStateMissingIsNotAnError(t *testing.T) {
	st, ok, err := ReadReturnState(t.TempDir(), "/some/clone")
	if err != nil || ok || st.V != 0 || len(st.NotHome) != 0 {
		t.Fatalf("st=%+v ok=%v err=%v; want zero, false, nil", st, ok, err)
	}
	// A data dir that does not exist yet is the same as no file.
	if _, ok, err := ReadReturnState(filepath.Join(t.TempDir(), "absent"), "/some/clone"); err != nil || ok {
		t.Fatalf("absent data dir: ok=%v err=%v", ok, err)
	}
}

func TestReturnStateRefusesSymlinkedDir(t *testing.T) {
	dd, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(dd, returnStateDir)); err != nil {
		t.Fatal(err)
	}
	err := WriteReturnState(dd, "/some/clone", ReturnState{NotHome: []NotHome{{Branch: "a", Reason: ReasonOff}}})
	if err == nil {
		t.Fatal("write through a symlinked clone-return dir succeeded")
	}
	ents, _ := os.ReadDir(target)
	if len(ents) != 0 {
		t.Fatalf("os.Root allowed a write through a symlink: %v", ents)
	}
	if err := ForgetNotHome(dd, "/some/clone", "a"); err == nil {
		t.Fatal("forget through a symlinked dir succeeded")
	}
	ents, _ = os.ReadDir(target)
	if len(ents) != 0 {
		t.Fatalf("forget created files outside the data dir: %v", ents)
	}
}

func TestReturnStateRefusesSymlinkedFile(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	if err := WriteReturnState(dd, dir, ReturnState{}); err != nil {
		t.Fatal(err)
	}
	h, _ := cloneKeyHash(dir)
	p := filepath.Join(dd, returnStateDir, h+".json")
	other := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(other, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	if err := os.Symlink(other, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadReturnState(dd, dir); err == nil {
		t.Fatal("read followed a symlinked state file")
	}
}

func TestReturnStateCapsSize(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	if err := WriteReturnState(dd, dir, ReturnState{}); err != nil {
		t.Fatal(err)
	}
	h, _ := cloneKeyHash(dir)
	p := filepath.Join(dd, returnStateDir, h+".json")
	if err := os.WriteFile(p, []byte(`{"v":1,"error":"`+strings.Repeat("a", maxReturnStateFile)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadReturnState(dd, dir); err == nil {
		t.Fatal("read accepted a file over the cap")
	}
}

func TestReturnStateCapsEntries(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	var in ReturnState
	for i := 0; i < 150; i++ {
		in.NotHome = append(in.NotHome, NotHome{Branch: fmt.Sprintf("b%03d", i), Reason: ReasonNotFF})
	}
	if err := WriteReturnState(dd, dir, in); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadReturnState(dd, dir)
	if err != nil || len(got.NotHome) != maxNotHome {
		t.Fatalf("len=%d err=%v; want %d", len(got.NotHome), err, maxNotHome)
	}
	if got.NotHome[0].Branch != "b000" || got.NotHome[maxNotHome-1].Branch != "b099" {
		t.Fatalf("kept the wrong entries: %s .. %s", got.NotHome[0].Branch, got.NotHome[maxNotHome-1].Branch)
	}
	// A hand-edited file with more entries is capped on read too.
	h, _ := cloneKeyHash(dir)
	p := filepath.Join(dd, returnStateDir, h+".json")
	var sb strings.Builder
	sb.WriteString(`{"v":1,"not_home":[`)
	for i := 0; i < 150; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"branch":"b%d","reason":"off"}`, i)
	}
	sb.WriteString(`]}`)
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err = ReadReturnState(dd, dir)
	if err != nil || len(got.NotHome) != maxNotHome {
		t.Fatalf("read len=%d err=%v; want %d", len(got.NotHome), err, maxNotHome)
	}
}

func TestReturnStateSanitizesOnRead(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	if err := WriteReturnState(dd, dir, ReturnState{}); err != nil {
		t.Fatal(err)
	}
	h, _ := cloneKeyHash(dir)
	p := filepath.Join(dd, returnStateDir, h+".json")
	raw := `{"v":1,"error":"e\u001b[31m","not_home":[{"branch":"a‮b","reason":"off","detail":"` +
		strings.Repeat("x", 500) + `"}]}`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadReturnState(dd, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != "e?[31m" || got.NotHome[0].Branch != "a?b" || len([]rune(got.NotHome[0].Detail)) != 200 {
		t.Fatalf("not sanitized: %+v", got)
	}
}

func TestForgetNotHome(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	at := time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	in := ReturnState{At: at, Base: shaN(7), Error: "keep", NotHome: []NotHome{
		{Branch: "a", Reason: ReasonOff}, {Branch: "b", Reason: ReasonNotFF}, {Branch: "c", Reason: ReasonFailed},
	}}
	if err := WriteReturnState(dd, dir, in); err != nil {
		t.Fatal(err)
	}
	if err := ForgetNotHome(dd, dir, "b"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadReturnState(dd, dir)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(got.NotHome) != 2 || got.NotHome[0].Branch != "a" || got.NotHome[1].Branch != "c" {
		t.Fatalf("entries %+v", got.NotHome)
	}
	if !got.At.Equal(at) || got.Base != in.Base || got.Error != "keep" || got.V != 1 {
		t.Fatalf("Forget changed more than the entry: %+v", got)
	}
	// No file for this clone: nil, and no file is made.
	other := "/another/clone"
	if err := ForgetNotHome(dd, other, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadReturnState(dd, other); ok {
		t.Fatal("Forget created a state file")
	}
}

func TestReturnStateWritersSerialize(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	var in ReturnState
	for i := 0; i < 20; i++ {
		in.NotHome = append(in.NotHome, NotHome{Branch: fmt.Sprintf("b%d", i), Reason: ReasonNotFF})
	}
	if err := WriteReturnState(dd, dir, in); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ForgetNotHome(dd, dir, fmt.Sprintf("b%d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := ReadReturnState(dd, dir)
	if err != nil || len(got.NotHome) != 0 {
		t.Fatalf("left %d entries, err=%v; a lost update", len(got.NotHome), err)
	}
	ents, _ := os.ReadDir(filepath.Join(dd, returnStateDir))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReturnStateUncommittedRoundTrip(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	if err := WriteReturnState(dd, dir, ReturnState{Uncommitted: intp(7), UncommittedCut: true}); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadReturnState(dd, dir)
	if err != nil || got.Uncommitted == nil || *got.Uncommitted != 7 || !got.UncommittedCut {
		t.Fatalf("got %+v, %v", got, err)
	}
	// Zero is a value, not an absence.
	if err := WriteReturnState(dd, dir, ReturnState{Uncommitted: intp(0)}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = ReadReturnState(dd, dir); got.Uncommitted == nil || *got.Uncommitted != 0 {
		t.Errorf("Uncommitted = %v, want 0", got.Uncommitted)
	}
}

func TestReturnStateOldFileLoads(t *testing.T) {
	dd, dir := t.TempDir(), "/some/clone"
	h, _ := cloneKeyHash(dir)
	if err := os.MkdirAll(filepath.Join(dd, returnStateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"v":1,"at":"2026-10-31T01:02:03Z","base":"main","not_home":[{"branch":"b","reason":"off"}]}`
	if err := os.WriteFile(filepath.Join(dd, returnStateDir, h+".json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadReturnState(dd, dir)
	if err != nil || !ok || got.V != 1 || got.Base != "main" || len(got.NotHome) != 1 {
		t.Fatalf("got %+v, %v, %v", got, ok, err)
	}
	if got.Uncommitted != nil || got.UncommittedCut {
		t.Errorf("Uncommitted = %v cut=%v, want unknown", got.Uncommitted, got.UncommittedCut)
	}
}
