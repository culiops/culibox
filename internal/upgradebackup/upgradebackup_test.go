package upgradebackup

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 13, 10, 30, 0, 0, time.UTC)

// snapshot maps every path under dir to its mode and (for files) contents, so
// two calls can prove the tree did not change.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		v := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(b)
		}
		m[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func opts(dir, version string) Options {
	return Options{DataDir: dir, Version: version, Commit: "abc", Now: t0}
}

func TestDevBuildSkips(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	res, err := Run(opts(r, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res, Result{}) {
		t.Fatalf("result = %+v, want zero", res)
	}
	if _, err := os.Lstat(filepath.Join(r, StampName)); !os.IsNotExist(err) {
		t.Fatalf("stamp written for a dev build: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(r, "backups")); !os.IsNotExist(err) {
		t.Fatalf("backups/ created for a dev build: %v", err)
	}
}

func TestSameVersionNoop(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	writeFile(t, r, StampName, "v1 abc\n")
	before := snapshot(t, r)
	res, err := Run(opts(r, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Taken {
		t.Fatalf("Taken = true for an unchanged version: %+v", res)
	}
	if after := snapshot(t, r); !reflect.DeepEqual(before, after) {
		t.Fatalf("tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

func TestFreshInstallStampsOnly(t *testing.T) {
	r := t.TempDir()
	o := opts(r, "v1")
	res, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Taken || res.Dir != "" {
		t.Fatalf("fresh install took a backup: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(r, StampName))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v1 abc\n" {
		t.Fatalf("stamp = %q", b)
	}
	info, _ := os.Stat(filepath.Join(r, StampName))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("stamp mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(r, "backups")); !os.IsNotExist(err) {
		t.Fatalf("backups/ created on a fresh install: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(r, "*.tmp*")); len(left) != 0 {
		t.Fatalf("temp file left behind: %v", left)
	}
}

func TestFreshInstallConfigCountsOnlyWhenIncluded(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "config.json", "{}")
	res, err := Run(opts(r, "v1"))
	if err != nil || res.Taken {
		t.Fatalf("config.json alone with IncludeConfig=false: %+v, %v", res, err)
	}
	r2 := t.TempDir()
	writeFile(t, r2, "config.json", "{}")
	o := opts(r2, "v1")
	o.IncludeConfig = true
	res, err = Run(o)
	if err != nil || !res.Taken {
		t.Fatalf("config.json with IncludeConfig=true: %+v, %v", res, err)
	}
}

func TestMissingStampIsUnknown(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	res, err := Run(opts(r, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken || res.From != "unknown" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.HasPrefix(filepath.Base(res.Dir), "unknown-to-v1-") {
		t.Fatalf("dir = %q", res.Dir)
	}
	if fi, err := os.Stat(res.Dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir: %v %v", fi, err)
	}
	b, _ := os.ReadFile(filepath.Join(r, StampName))
	if string(b) != "v1 abc\n" {
		t.Fatalf("stamp = %q", b)
	}
}

func TestMalformedStampIsUnknown(t *testing.T) {
	for _, stamp := range []string{"", "\n", "   \n", "\x00\xff\n"} {
		r := t.TempDir()
		writeFile(t, r, "profiles.json", "{}")
		writeFile(t, r, StampName, stamp)
		res, err := Run(opts(r, "v1"))
		if err != nil {
			t.Fatalf("stamp %q: %v", stamp, err)
		}
		if !res.Taken || res.From != "unknown" {
			t.Fatalf("stamp %q: result = %+v", stamp, res)
		}
	}
}

func TestVersionChangeKeepsFrom(t *testing.T) {
	r := t.TempDir()
	writeFile(t, r, "profiles.json", "{}")
	writeFile(t, r, StampName, "v0.9 def\n")
	res, err := Run(opts(r, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Taken || res.From != "v0.9" || !strings.HasPrefix(filepath.Base(res.Dir), "v0.9-to-v1-") {
		t.Fatalf("result = %+v", res)
	}
}

func TestDirNameSanitized(t *testing.T) {
	// 23:59:59 at UTC-5 is already the next day in UTC.
	loc := time.FixedZone("x", -5*3600)
	at := time.Date(2026, 11, 13, 23, 59, 59, 0, loc)
	got := dirName("v1.2.3-4-gabc-dirty", "a/b c", at)
	re := regexp.MustCompile(`^[A-Za-z0-9._-]+-to-[A-Za-z0-9._-]+-\d{8}T\d{6}Z$`)
	if !re.MatchString(got) {
		t.Fatalf("dirName = %q", got)
	}
	if strings.Contains(got, "/") {
		t.Fatalf("dirName has a slash: %q", got)
	}
	if want := "v1.2.3-4-gabc-dirty-to-a_b_c-20261114T045959Z"; got != want {
		t.Fatalf("dirName = %q, want %q", got, want)
	}
}
