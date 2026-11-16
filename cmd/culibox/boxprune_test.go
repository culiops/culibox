package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	boximage "github.com/culiops/culibox/docker/agent-box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/dockerfake"
)

const (
	pIDNew   = "sha256:aaaa0000000000000000000000000000000000000000000000000000000000aa"
	pIDOld   = "sha256:bbbb0000000000000000000000000000000000000000000000000000000000bb"
	pIDStop  = "sha256:cccc0000000000000000000000000000000000000000000000000000000000cc"
	pIDOther = "sha256:dddd0000000000000000000000000000000000000000000000000000000000dd"
	pIDForgn = "sha256:eeee0000000000000000000000000000000000000000000000000000000000ee"
	pDigNew  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	pDigOld  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	pDigStop = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	pDigOthr = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	ghcr     = "ghcr.io/culiops/culibox"
)

func cfgJSON(image string) string {
	if image == "" {
		return "{}"
	}
	return `{"box_image": "` + image + `"}`
}

func pulled(id string, size int64, digest string) dockerfake.Image {
	return dockerfake.Image{ID: id, Size: size, Labeled: true, Hash: boximage.Hash(),
		Refs: []dockerfake.Ref{{Repo: ghcr, Digest: digest}}}
}

// A home with this instance's config at the default root, and the places other
// instances on the same Docker keep theirs.
type instHome struct {
	home, root, xdg string
}

func newInstHome(t *testing.T) instHome {
	t.Helper()
	home := t.TempDir()
	return instHome{home: home, root: filepath.Join(home, ".local", "share", "culibox"), xdg: filepath.Join(home, ".config")}
}

func (h instHome) src(cfg config.Config, cfgFiles ...string) pinSource {
	return pinSource{cfg: cfg, home: h.home, dataDir: h.root, xdg: h.xdg, cfgFiles: cfgFiles}
}

func TestVisiblePinsReadEveryConfigThisCuliBoxCanSee(t *testing.T) {
	h := newInstHome(t)
	other := filepath.Join(t.TempDir(), "dev.json")
	writeFile(t, filepath.Join(h.root, "config.json"), cfgJSON(ghcr+"@"+pDigOld))
	// The pre-rename default root and both ~/.config spellings are other places an
	// instance's config lives.
	writeFile(t, filepath.Join(h.home, ".local", "share", "agent-box", "config.json"), cfgJSON("culiops/agent-box:2026-09-01"))
	writeFile(t, filepath.Join(h.xdg, "culibox", "config.json"), cfgJSON("culiops/culibox:2026-10-01"))
	writeFile(t, filepath.Join(h.xdg, "agent-box", "config.json"), cfgJSON("culiops/culibox:2026-10-02"))
	// An explicit -config file, and a file that does not exist.
	writeFile(t, other, cfgJSON("registry.example.com/team/box:7"))
	// A retained upgrade backup's config: a rollback restores it.
	writeFile(t, filepath.Join(h.root, "backups", "0.1-to-0.2-20261101T000000Z", "config.json"), cfgJSON("culiops/culibox:2026-08-01"))
	pins, err := h.src(config.Config{BoxImage: "culiops/culibox:2026-11-16"}, other, filepath.Join(t.TempDir(), "absent.json")).pins()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ghcr + "@" + pDigOld, "culiops/agent-box:2026-09-01", "culiops/culibox:2026-10-01",
		"culiops/culibox:2026-10-02", "registry.example.com/team/box:7", "culiops/culibox:2026-11-16"} {
		if !contains(pins.live, want) {
			t.Errorf("live pins lack %q: %v", want, pins.live)
		}
	}
	if len(pins.rollback) != 1 || pins.rollback[0] != "culiops/culibox:2026-08-01" {
		t.Errorf("rollback pins %v", pins.rollback)
	}
	if !contains(pins.repos, "registry.example.com/team/box") || !contains(pins.repos, "culiops/culibox") {
		t.Errorf("repos %v", pins.repos)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// A config that is there but cannot be read is an instance whose pin is unknown:
// the answer is "do not prune", never "it pins nothing".
func TestVisiblePinsFailClosedOnAConfigThatCannotBeRead(t *testing.T) {
	h := newInstHome(t)
	writeFile(t, filepath.Join(h.root, "config.json"), "{not json")
	if _, err := h.src(config.Config{}).pins(); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("err %v", err)
	}
	h2 := newInstHome(t)
	writeFile(t, filepath.Join(h2.xdg, "culibox", "config.json"), "{}")
	if err := os.Chmod(filepath.Join(h2.xdg, "culibox", "config.json"), 0); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 { // root reads anything
		if _, err := h2.src(config.Config{}).pins(); err == nil {
			t.Fatal("an unreadable config was skipped")
		}
	}
}

// The scenario of the issue, end to end over the fake docker: the config now
// pins a new digest; box setup pulls it; the previous one goes.
type upgradeStore struct {
	h instHome
	f *dockerfake.Fake
}

func newUpgradeStore(t *testing.T) upgradeStore {
	h := newInstHome(t)
	f := dockerfake.New(t)
	newRef := ghcr + "@" + pDigNew
	f.PullAdds(newRef, pulled(pIDNew, 4_200_000_000, pDigNew))
	f.Add(pulled(pIDOld, 4_100_000_000, pDigOld))   // the previous pin: unreferenced now
	f.Add(pulled(pIDStop, 4_000_000_000, pDigStop)) // used by a stopped container
	f.Container("stopped-box", pIDStop)
	f.Add(pulled(pIDOther, 3_900_000_000, pDigOthr)) // pinned by another instance's config
	writeFile(t, filepath.Join(h.xdg, "culibox", "config.json"), cfgJSON(ghcr+"@"+pDigOthr))
	// Not a box image at all.
	f.Add(dockerfake.Image{ID: pIDForgn, Size: 900_000_000, Refs: []dockerfake.Ref{{Repo: "postgres", Tag: "16"}}})
	return upgradeStore{h: h, f: f}
}

func (u upgradeStore) setup(t *testing.T, noPrune bool) (int, string) {
	t.Helper()
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@" + pDigNew}
	rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: u.f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Cmd: "box", Sub: "setup", NoPrune: noPrune}, u.h.src(cfg), setupNow, "linux", &w)
	return rc, w.String()
}

func TestBoxSetupPrunesOldImagesAfterAPull(t *testing.T) {
	u := newUpgradeStore(t)
	rc, out := u.setup(t, false)
	if rc != 0 {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if u.f.Has(pIDOld) {
		t.Fatalf("the old unreferenced image is still there:\n%s", out)
	}
	for id, why := range map[string]string{pIDNew: "the image just set up", pIDStop: "a stopped container uses it",
		pIDOther: "another instance's config pins it", pIDForgn: "it is not a box image"} {
		if !u.f.Has(id) {
			t.Errorf("%s was removed: %s", id, why)
		}
	}
	// The output names each removed image and the bytes.
	for _, want := range []string{"PASS image: pulled " + ghcr + "@" + pDigNew,
		"removed old box image " + ghcr + "@" + pDigOld, "4100000000 bytes", "4.1 GB", "freeing up to 4100000000 bytes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{pDigStop, pDigOthr, "postgres"} {
		if strings.Contains(strings.SplitN(out, "PASS image:", 2)[1], bad) {
			t.Errorf("output names a kept image %q:\n%s", bad, out)
		}
	}
}

func TestBoxSetupNoPruneKeepsEverything(t *testing.T) {
	u := newUpgradeStore(t)
	if rc, out := u.setup(t, true); rc != 0 || !u.f.Has(pIDOld) || u.f.Count("image rm") != 0 || u.f.Count("image ls") != 0 {
		t.Fatalf("rc %d, old present %v:\n%s\n%s", rc, u.f.Has(pIDOld), out, u.f.Log())
	}
}

// A setup that did not pull or build anything prunes nothing.
func TestBoxSetupFailureDoesNotPrune(t *testing.T) {
	u := newUpgradeStore(t)
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@sha256:" + strings.Repeat("9", 64)} // the registry refuses it
	rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: u.f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Cmd: "box", Sub: "setup"}, u.h.src(cfg), setupNow, "linux", &w)
	if rc != 1 || !u.f.Has(pIDOld) || u.f.Count("image rm") != 0 {
		t.Fatalf("rc %d, old present %v:\n%s", rc, u.f.Has(pIDOld), w.String())
	}
}

// A failure to remove one image is told, and setup still succeeds.
func TestBoxSetupPruneFailureIsReportedNotFatal(t *testing.T) {
	u := newUpgradeStore(t)
	u.f.FailRm(ghcr + "@" + pDigOld)
	rc, out := u.setup(t, false)
	if rc != 0 {
		t.Fatalf("a failed removal failed setup (rc %d):\n%s", rc, out)
	}
	if !strings.Contains(out, "WARN image: could not remove old box image "+ghcr+"@"+pDigOld) ||
		!strings.Contains(out, "simulated failure") || strings.Contains(out, "removed old box image") {
		t.Fatalf("output:\n%s", out)
	}
}

// Prune trouble of any kind (a config it cannot read) is a warning; the pull stands.
func TestBoxSetupPruneSkippedWhenAConfigCannotBeRead(t *testing.T) {
	u := newUpgradeStore(t)
	writeFile(t, filepath.Join(u.h.root, "config.json"), "{broken")
	rc, out := u.setup(t, false)
	if rc != 0 || !u.f.Has(pIDOld) || u.f.Count("image rm") != 0 {
		t.Fatalf("rc %d old present %v:\n%s", rc, u.f.Has(pIDOld), out)
	}
	if !strings.Contains(out, "WARN image: old box images not removed") || !strings.Contains(out, "config.json") {
		t.Fatalf("output:\n%s", out)
	}
}

// The names an instance runs with no box_image, and the pre-rename one a
// not-yet-upgraded CuliBox runs, are pinned whatever configs are visible.
func TestPruneKeepsTheDefaultImageNames(t *testing.T) {
	h := newInstHome(t)
	f := dockerfake.New(t)
	f.PullAdds(ghcr+"@"+pDigNew, pulled(pIDNew, 1, pDigNew))
	repoOf := func(ref string) string { r, _ := boximage.Repo(ref); return r }
	_, defTag, _ := strings.Cut(config.DefaultBoxImage, ":")
	_, legTag, _ := strings.Cut(config.LegacyDefaultBoxImage, ":")
	f.Add(dockerfake.Image{ID: pIDOld, Size: 1, Labeled: true, Refs: []dockerfake.Ref{{Repo: repoOf(config.DefaultBoxImage), Tag: defTag}}})
	f.Add(dockerfake.Image{ID: pIDStop, Size: 1, Labeled: true, Refs: []dockerfake.Ref{{Repo: repoOf(config.LegacyDefaultBoxImage), Tag: legTag}}})
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@" + pDigNew}
	if rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Sub: "setup"}, h.src(cfg), setupNow, "linux", &w); rc != 0 {
		t.Fatalf("rc %d:\n%s", rc, w.String())
	}
	if !f.Has(pIDOld) || !f.Has(pIDStop) || f.Count("image rm") != 0 {
		t.Fatalf("a default-named image was removed:\n%s\n%s", w.String(), f.Log())
	}
}

// Both repositories the config constants name must be recognised as box images.
func TestBoxReposCoverTheConfigDefaults(t *testing.T) {
	for _, img := range []string{config.DefaultBoxImage, config.LegacyDefaultBoxImage} {
		if r, _ := boximage.Repo(img); !contains(boximage.BoxRepos, r) {
			t.Errorf("%s is not in BoxRepos", r)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 kB", 4_100_000_000: "4.1 GB", 12_300_000: "12.3 MB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestParseNoPrune(t *testing.T) {
	a, err := parseArgs([]string{"box", "setup", "-no-prune"})
	if err != nil || !a.NoPrune {
		t.Fatalf("%+v %v", a, err)
	}
	if a, err = parseArgs([]string{"box", "setup"}); err != nil || a.NoPrune {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err = parseArgs([]string{"box", "status", "-no-prune"}); err == nil {
		t.Fatal("-no-prune accepted outside box setup")
	}
	if _, err = parseArgs([]string{"doctor", "-no-prune"}); err == nil {
		t.Fatal("-no-prune accepted outside box setup")
	}
}

// macOS: box setup asks the VM's daemon (the pinned --host), not the user's
// docker, and a prune that cannot run (this fake answers no image listing) is a
// warning: setup still ends 0.
func TestDarwinBoxSetupPrunesThroughTheVMDaemonAndNeverFailsSetup(t *testing.T) {
	l, _, d := readySetup(t, "default")
	rc, out := runVM(t, l, cliArgs{Sub: "setup"})
	if rc != 0 {
		t.Fatalf("setup = %d:\n%s", rc, out)
	}
	if want := strings.Join(l.dockerArgs, " ") + " image ls"; !strings.Contains(d.log(), want) {
		t.Fatalf("no %q in the docker log:\n%s", want, d.log())
	}
	if !strings.Contains(out, "WARN image: old box images not removed") {
		t.Fatalf("a failed prune is not told:\n%s", out)
	}
	l, _, d = readySetup(t, "default")
	if rc, out = runVM(t, l, cliArgs{Sub: "setup", NoPrune: true}); rc != 0 || strings.Contains(d.log(), " image ls") {
		t.Fatalf("-no-prune: rc %d, log:\n%s", rc, d.log())
	}
}
