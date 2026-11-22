package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	dockerfilePath = "../../docker/culibox/Dockerfile"
	docPath        = "../../docs/BOX-IMAGE.md"
)

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The doc is generated, never hand-kept: a Dockerfile change that is not
// regenerated fails here.
func TestDocIsCurrent(t *testing.T) {
	got, err := Render(readFile(t, dockerfilePath))
	if err != nil {
		t.Fatal(err)
	}
	if want := string(readFile(t, docPath)); got != want {
		t.Error("docs/BOX-IMAGE.md is out of date with docker/culibox/Dockerfile; run: go run ./tools/boximage-doc -w")
	}
}

// A new pin cannot ship undescribed, and a removed one cannot leave a stale row.
func TestEveryVersionArgIsDescribed(t *testing.T) {
	var args []string
	for _, m := range regexp.MustCompile(`(?m)^ARG ([A-Z0-9_]+)_VERSION=`).FindAllStringSubmatch(string(readFile(t, dockerfilePath)), -1) {
		args = append(args, m[1])
	}
	for _, a := range args {
		if _, ok := tools[a]; !ok {
			t.Errorf("ARG %s_VERSION has no entry in tools", a)
		}
	}
	for k := range tools {
		if !slices.Contains(args, k) {
			t.Errorf("tools entry %s has no ARG %s_VERSION in the Dockerfile", k, k)
		}
	}
}

func TestRenderListsPinsAndPackages(t *testing.T) {
	got, err := Render(readFile(t, dockerfilePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| kubectl | 1.37.1 |",
		"a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5",
		"`gcc`", "`libc6-dev`", "`chromium`", "`zsh`", "`git`",
		"| `playwright` |",
		"CGO_ENABLED=1 go test -race",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered doc lacks %q", want)
		}
	}
	// gcc in the abduco-build stage is not in the image; only the final stage counts.
	apt := got[strings.Index(got, "## Debian packages"):]
	apt = apt[:strings.Index(apt[1:], "\n## ")+1]
	if n := strings.Count(apt, "`gcc`"); n != 1 {
		t.Errorf("gcc listed %d times in Debian packages, want once:\n%s", n, apt)
	}
	if strings.Contains(apt, "`make`, `libc6-dev`") || strings.Contains(apt, "`curl`, `ca-certificates`, `make`") {
		t.Errorf("Debian packages picked up the abduco-build stage:\n%s", apt)
	}
}

func TestRenderFailsOnMissingInfo(t *testing.T) {
	df := "FROM node:24-bookworm-slim@sha256:" + strings.Repeat("a", 64) + "\nARG NEWTOOL_VERSION=1\n"
	if _, err := Render([]byte(df)); err == nil || !strings.Contains(err.Error(), "NEWTOOL") {
		t.Fatalf("err = %v, want one naming NEWTOOL", err)
	}
}

// An apt install the parser cannot read must stop the generator, never vanish
// from the doc.
func TestRenderFailsOnUnreadableAptInstall(t *testing.T) {
	df := "FROM node:24-bookworm-slim@sha256:" + strings.Repeat("a", 64) + "\nRUN apt-get update && apt-get install -y foo\n"
	if _, err := Render([]byte(df)); err == nil || !strings.Contains(err.Error(), "--no-install-recommends") {
		t.Fatalf("err = %v, want one about --no-install-recommends", err)
	}
}
