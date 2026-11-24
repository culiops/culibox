package boximagecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boxregistry"
	"github.com/culiops/culibox/internal/datacompat"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

const goodRef = "ghcr.io/culiops/culibox@sha256:0000000000000000000000000000000000000000000000000000000000000001"

func writeCache(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, CacheFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Cache{Version: 1, CheckedAt: t0, Hash: boximage.Hash(),
		Registry: &boxregistry.Index{Ref: goodRef, Platforms: map[string]boxregistry.Platform{
			"amd64": {Created: t0, ClaudeCode: "2.1.311", Layers: []boxregistry.Layer{{Digest: "sha256:" + strings.Repeat("a", 64), Size: 5}}, DiffIDs: []string{"sha256:" + strings.Repeat("b", 64)}}}},
		Scan: &Scan{Number: 357, Count: 680}}
	if err := SaveCache(dir, want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, CacheFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, CacheFile)); !strings.Contains(string(raw), `"version":1`) {
		t.Errorf("version 1 is not on disk: %s", raw)
	}
	got, err := LoadCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Version != 1 || !got.CheckedAt.Equal(t0) || got.Hash != want.Hash ||
		got.Registry == nil || got.Registry.Ref != goodRef || got.Registry.Platforms["amd64"].ClaudeCode != "2.1.311" ||
		len(got.Registry.Platforms["amd64"].Layers) != 1 || got.Scan == nil || *got.Scan != *want.Scan {
		t.Fatalf("got %+v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, CacheFile+".tmp.*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestLoadCacheAbsent(t *testing.T) {
	if c, err := LoadCache(t.TempDir()); c != nil || err != nil {
		t.Fatalf("got %v, %v", c, err)
	}
	if c, err := LoadCache(filepath.Join(t.TempDir(), "nope")); c != nil || err != nil {
		t.Fatalf("no data dir: got %v, %v", c, err)
	}
}

// A file that cannot be read is simply due: the check writes a fresh one.
func TestCacheGarbageDue(t *testing.T) {
	for name, content := range map[string]string{
		"not json": "{{{", "empty": "", "version 0": `{"checked_at":"2026-11-24T10:00:00Z"}`,
		"wrong type": `{"version":1,"checked_at":5}`,
	} {
		dir := t.TempDir()
		writeCache(t, dir, content)
		c, err := LoadCache(dir)
		if c != nil || err != nil {
			t.Errorf("%s: got %v, %v", name, c, err)
		}
		if !Due(c, t0, false) {
			t.Errorf("%s: not due", name)
		}
	}
}

func TestCacheNewerKept(t *testing.T) {
	dir := t.TempDir()
	const newer = `{"version":99,"checked_at":"2026-11-24T10:00:00Z","hash":"x","future":{"a":1}}`
	writeCache(t, dir, newer)
	if c, err := LoadCache(dir); c != nil || !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("Load: %v, %v", c, err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	k := Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0 }}
	c, err := RunIfDue(context.Background(), k, dir, Inputs{CheckOn: true, Explicit: true}, false)
	if c != nil || !errors.Is(err, datacompat.ErrNewer) {
		t.Fatalf("RunIfDue: %v, %v", c, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, CacheFile)); string(b) != newer {
		t.Errorf("the newer file was rewritten: %s", b)
	}
	if calls.Load() != 0 {
		t.Errorf("%d requests", calls.Load())
	}
}

// What is read from disk is validated before anything shows it.
func TestLoadCacheValidates(t *testing.T) {
	dir := t.TempDir()
	writeCache(t, dir, `{"version":1,"checked_at":"2026-11-24T10:00:00Z","hash":"h",
		"registry":{"ref":"ghcr.io/evil/x@sha256:00"},"scan":{"number":1000000,"count":1}}`)
	c, err := LoadCache(dir)
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	if c.Registry != nil || c.Scan != nil {
		t.Errorf("kept %+v / %+v", c.Registry, c.Scan)
	}
	for name, scan := range map[string]string{
		"negative number": `{"number":-1,"count":2}`, "negative count": `{"number":1,"count":-2}`,
		"big count": `{"number":1,"count":1000000}`, "big number": `{"number":1000000,"count":1}`,
	} {
		writeCache(t, dir, `{"version":1,"checked_at":"2026-11-24T10:00:00Z","hash":"h","scan":`+scan+`}`)
		if c, _ := LoadCache(dir); c == nil || c.Scan != nil {
			t.Errorf("%s: kept %+v", name, c)
		}
	}
	// {0,0} is "none open": it must survive
	writeCache(t, dir, `{"version":1,"checked_at":"2026-11-24T10:00:00Z","hash":"h","scan":{"number":0,"count":0}}`)
	if c, _ := LoadCache(dir); c == nil || c.Scan == nil || *c.Scan != (Scan{}) {
		t.Errorf("none open dropped: %+v", c)
	}
	writeCache(t, dir, `{"version":1,"checked_at":"2026-11-24T10:00:00Z","hash":"h","registry":{"ref":"`+goodRef+`"},"scan":{"number":999999,"count":999999}}`)
	if c, _ := LoadCache(dir); c.Registry == nil || c.Scan == nil {
		t.Errorf("valid values dropped: %+v", c)
	}
}

// Everything the cache can hand to doctor or the UI is checked on the way in.
func TestLoadCacheSanitizesWhatItShows(t *testing.T) {
	dir := t.TempDir()
	good := "sha256:" + strings.Repeat("a", 64)
	reg := func(platform string) string {
		return `"registry":{"ref":"` + goodRef + `","platforms":{"amd64":` + platform + `}}`
	}
	load := func(rest string) *Cache {
		t.Helper()
		writeCache(t, dir, `{"version":1,"checked_at":"2026-11-24T10:00:00Z","hash":"h",`+rest+`}`)
		c, err := LoadCache(dir)
		if err != nil || c == nil {
			t.Fatal(c, err)
		}
		return c
	}
	// error text: only the classes survive, anything else becomes the generic class
	for in, want := range map[string]string{
		"": "", "registry unreachable": "registry unreachable", "not published yet": "not published yet",
		"unexpected registry answer": "unexpected registry answer", "scan unreadable": "unexpected registry answer",
		"<script>alert(1)</script>": "unexpected registry answer",
	} {
		if got := load(`"registry_err":"` + in + `"`).RegistryErr; got != want {
			t.Errorf("registry_err %q -> %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"": "", "scan unreadable": "scan unreadable", "registry unreachable": "scan unreadable", "boom <b>": "scan unreadable",
	} {
		if got := load(`"scan_err":"` + in + `"`).ScanErr; got != want {
			t.Errorf("scan_err %q -> %q, want %q", in, got, want)
		}
	}
	// platform facts
	p := load(reg(`{"claude_code":"2.1.311","layers":[{"digest":"` + good + `","size":5}],"diff_ids":["` + good + `"]}`)).Registry.Platforms["amd64"]
	if p.ClaudeCode != "2.1.311" || len(p.Layers) != 1 || len(p.DiffIDs) != 1 {
		t.Errorf("valid facts dropped: %+v", p)
	}
	for name, plat := range map[string]string{
		"claude code":   `{"claude_code":"<img src=x>"}`,
		"layer size":    `{"layers":[{"digest":"` + good + `","size":-1}],"diff_ids":["` + good + `"]}`,
		"layer too big": `{"layers":[{"digest":"` + good + `","size":1099511627777}],"diff_ids":["` + good + `"]}`,
		"layer digest":  `{"layers":[{"digest":"sha256:zz","size":1}],"diff_ids":["` + good + `"]}`,
		"diff id":       `{"layers":[{"digest":"` + good + `","size":1}],"diff_ids":["nope"]}`,
		"diff id count": `{"layers":[{"digest":"` + good + `","size":1}],"diff_ids":["` + good + `","` + good + `"]}`,
	} {
		p := load(reg(plat)).Registry.Platforms["amd64"]
		switch name {
		case "claude code":
			if p.ClaudeCode != "" {
				t.Errorf("%s kept: %+v", name, p)
			}
		case "layer size", "layer too big", "layer digest":
			if p.Layers != nil || p.DiffIDs != nil {
				t.Errorf("%s kept: %+v", name, p)
			}
		default: // the layers stand, only the diff ids go
			if p.Layers == nil || p.DiffIDs != nil {
				t.Errorf("%s: %+v", name, p)
			}
		}
	}
}

func TestDue(t *testing.T) {
	fresh := func() *Cache { return &Cache{Version: 1, CheckedAt: t0, Hash: boximage.Hash()} }
	other := fresh()
	other.Hash = "000000000000"
	failed := fresh()
	failed.RegistryErr = "registry unreachable"
	for _, tc := range []struct {
		name  string
		c     *Cache
		now   time.Time
		retry bool
		want  bool
	}{
		{"absent", nil, t0, false, true},
		{"fresh", fresh(), t0.Add(time.Hour), false, false},
		{"23h59", fresh(), t0.Add(24*time.Hour - time.Minute), false, false},
		{"25h", fresh(), t0.Add(25 * time.Hour), false, true},
		{"other hash", other, t0.Add(time.Minute), false, true},
		{"failed, no retry", failed, t0.Add(time.Minute), false, false},
		{"failed, retry", failed, t0.Add(time.Minute), true, true},
		{"ok, retry", fresh(), t0.Add(time.Minute), true, false},
		{"checked in the future", fresh(), t0.Add(-time.Hour), false, true}, // a skewed clock must not park the check for good
	} {
		if got := Due(tc.c, tc.now, tc.retry); got != tc.want {
			t.Errorf("%s: Due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRunIfDueNoNetworkForReleaseDefault(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	k := Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0 }}
	dir := t.TempDir()
	for name, in := range map[string]Inputs{
		"release default": {CheckOn: true, Published: goodRef},
		"check off":       {CheckOn: false, Explicit: true},
	} {
		c, err := RunIfDue(context.Background(), k, dir, in, false)
		if c != nil || err != nil {
			t.Errorf("%s: %v, %v", name, c, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, CacheFile)); err == nil {
		t.Error("a cache was written")
	}
}

func TestRunIfDueNotDueKeepsCache(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	dir := t.TempDir()
	if err := SaveCache(dir, &Cache{Version: 1, CheckedAt: t0, Hash: boximage.Hash(), Scan: &Scan{1, 2}}); err != nil {
		t.Fatal(err)
	}
	k := Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0.Add(time.Hour) }}
	c, err := RunIfDue(context.Background(), k, dir, Inputs{CheckOn: true, Explicit: true}, false)
	if err != nil || c == nil || c.Scan == nil || *c.Scan != (Scan{1, 2}) {
		t.Fatalf("%+v, %v", c, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests", calls.Load())
	}
}

func TestRunIfDueChecksAndSaves(t *testing.T) {
	srv, _ := fakeWorld(t, worldOpts{issues: scanBody(357, 680)})
	k := Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0 }}
	dir := t.TempDir()
	c, err := RunIfDue(context.Background(), k, dir, Inputs{CheckOn: true, Published: ""}, false)
	if err != nil || c == nil || c.Registry == nil || c.Scan == nil || *c.Scan != (Scan{357, 680}) {
		t.Fatalf("%+v, %v", c, err)
	}
	if got, err := LoadCache(dir); err != nil || got == nil || got.Registry == nil || got.Registry.Ref != c.Registry.Ref {
		t.Fatalf("saved %+v, %v", got, err)
	}
	// a failed attempt is kept too: one try per day, whatever happened
	failing := Checker{Client: srv.Client(), Registry: "http://127.0.0.1:1", GitHubAPI: "http://127.0.0.1:1", Now: func() time.Time { return t0.Add(48 * time.Hour) }}
	c, err = RunIfDue(context.Background(), failing, dir, Inputs{CheckOn: true, Explicit: true}, false)
	if err != nil || c.RegistryErr != "registry unreachable" {
		t.Fatalf("%+v, %v", c, err)
	}
	if got, _ := LoadCache(dir); got == nil || got.RegistryErr != "registry unreachable" {
		t.Fatalf("saved %+v", got)
	}
}

// Shutdown cancels the context mid-check: the "unreachable" that produces is not
// an answer, and saving it would park the check for a day.
func TestRunIfDueCancelledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	k := Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0 }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunIfDue(ctx, k, dir, Inputs{CheckOn: true, Explicit: true}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, CacheFile)); !os.IsNotExist(err) {
		t.Fatalf("a cache was written for a cancelled check: %v", err)
	}
}

// A deadline that runs out is a real failure and is saved.
func TestRunIfDueDeadlineIsSaved(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	k := Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0 }}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c, err := RunIfDue(ctx, k, dir, Inputs{CheckOn: true, Explicit: true}, false)
	if err != nil || c == nil || c.RegistryErr == "" {
		t.Fatalf("RunIfDue: %v, %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(dir, CacheFile)); err != nil {
		t.Fatalf("no cache for a timed-out check: %v", err)
	}
}
