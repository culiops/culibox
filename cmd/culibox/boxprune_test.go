package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/dockerfake"
)

const (
	pIDNew   = "sha256:aaaa0000000000000000000000000000000000000000000000000000000000aa"
	pIDOld   = "sha256:bbbb0000000000000000000000000000000000000000000000000000000000bb"
	pIDStop  = "sha256:cccc0000000000000000000000000000000000000000000000000000000000cc"
	pIDOther = "sha256:dddd0000000000000000000000000000000000000000000000000000000000dd"
	pIDForgn = "sha256:eeee0000000000000000000000000000000000000000000000000000000000ee"
	pIDLocal = "sha256:ffff0000000000000000000000000000000000000000000000000000000000ff"
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

func local(id string, size int64, refs ...dockerfake.Ref) dockerfake.Image {
	return dockerfake.Image{ID: id, Size: size, Labeled: true, Hash: boximage.Hash(), Refs: refs}
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
	// Retained upgrade backups' configs: a rollback restores them. A dated tag is
	// only ever built here, so it can only be kept; a registry digest can be pulled.
	writeFile(t, filepath.Join(h.root, "backups", "0.1-to-0.2-20261101T000000Z", "config.json"), cfgJSON("culiops/culibox:2026-08-01"))
	writeFile(t, filepath.Join(h.root, "backups", "0.2-to-0.3-20261105T000000Z", "config.json"), cfgJSON(ghcr+"@"+pDigStop))
	writeFile(t, filepath.Join(h.root, "backups", "0.3-to-0.4-20261110T000000Z", "config.json"), cfgJSON(""))
	pins, err := h.src(config.Config{BoxImage: "culiops/culibox:2026-11-16"}, other, filepath.Join(t.TempDir(), "absent.json")).pins(context.Background())
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
		t.Errorf("rollback pins %v, want only the dated tag (a digest can be pulled again)", pins.rollback)
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
	if _, err := h.src(config.Config{}).pins(context.Background()); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("err %v", err)
	}
	h2 := newInstHome(t)
	writeFile(t, filepath.Join(h2.xdg, "culibox", "config.json"), "{}")
	if err := os.Chmod(filepath.Join(h2.xdg, "culibox", "config.json"), 0); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 { // root reads anything
		if _, err := h2.src(config.Config{}).pins(context.Background()); err == nil {
			t.Fatal("an unreadable config was skipped")
		}
	}
}

// Only regular files are read: a FIFO where a config would be (or in a backup)
// must not block `box setup`.
func TestVisiblePinsDoNotReadAFifo(t *testing.T) {
	h := newInstHome(t)
	fifo := filepath.Join(h.xdg, "culibox", "config.json")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no fifo here: %v", err)
	}
	bk := filepath.Join(h.root, "backups", "x-to-y-20261101T000000Z", "config.json")
	if err := os.MkdirAll(filepath.Dir(bk), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(bk, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.src(config.Config{}).pins(ctx); err != nil {
		t.Fatalf("a fifo: %v", err)
	}
}

// Reading the configs is bounded by the prune's deadline.
func TestVisiblePinsHonourTheDeadline(t *testing.T) {
	h := newInstHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.src(config.Config{}).pins(ctx); err == nil {
		t.Fatal("a dead context was ignored")
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
	f.Add(pulled(pIDOld, 4_100_000_000, pDigOld))   // the previous pin: unreferenced now, re-pullable
	f.Add(pulled(pIDStop, 4_000_000_000, pDigStop)) // used by a stopped container
	f.Container("stopped-box", pIDStop)
	f.Add(pulled(pIDOther, 3_900_000_000, pDigOthr)) // pinned by another instance's config
	writeFile(t, filepath.Join(h.xdg, "culibox", "config.json"), cfgJSON(ghcr+"@"+pDigOthr))
	// A local build nobody uses: could be another instance's pin, cannot be pulled again.
	f.Add(local(pIDLocal, 3_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-09-01"}))
	// Not a box image at all.
	f.Add(dockerfake.Image{ID: pIDForgn, Size: 900_000_000, Refs: []dockerfake.Ref{{Repo: "postgres", Tag: "16"}}})
	return upgradeStore{h: h, f: f}
}

func (u upgradeStore) setup(t *testing.T, a cliArgs) (int, string) {
	t.Helper()
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@" + pDigNew}
	a.Cmd, a.Sub = "box", "setup"
	rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: u.f.Bin}, cfg.BoxImageOrDefault(), a, u.h.src(cfg), setupNow, "linux", &w)
	return rc, w.String()
}

func TestBoxSetupPrunesOldRePullableImagesAfterAPull(t *testing.T) {
	u := newUpgradeStore(t)
	rc, out := u.setup(t, cliArgs{})
	if rc != 0 {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if u.f.Has(pIDOld) {
		t.Fatalf("the old re-pullable image is still there:\n%s", out)
	}
	for id, why := range map[string]string{pIDNew: "the image just set up", pIDStop: "a stopped container uses it",
		pIDOther: "another instance's config pins it", pIDForgn: "it is not a box image",
		pIDLocal: "a local build is only removed on request"} {
		if !u.f.Has(id) {
			t.Errorf("%s was removed: %s", id, why)
		}
	}
	// The output names each removed image and the bytes, then lists what it kept.
	for _, want := range []string{"PASS image: pulled " + ghcr + "@" + pDigNew,
		"removed old box image " + ghcr + "@" + pDigOld, "4100000000 bytes", "4.1 GB", "freeing up to 4100000000 bytes",
		"kept 1 unused local box image(s), 3.0 GB", "culiops/culibox:2026-09-01 (3.0 GB): `docker image rm " + pIDLocal + "`",
		"`culibox box setup -prune-local`"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	after := strings.SplitN(out, "PASS image:", 2)[1]
	for _, bad := range []string{pDigStop, pDigOthr, "postgres"} {
		if strings.Contains(after, bad) {
			t.Errorf("output names a kept image %q:\n%s", bad, out)
		}
	}
}

func TestBoxSetupPruneLocalRemovesTheUnusedLocalBuildsToo(t *testing.T) {
	u := newUpgradeStore(t)
	rc, out := u.setup(t, cliArgs{PruneLocal: true})
	if rc != 0 || u.f.Has(pIDLocal) || u.f.Has(pIDOld) {
		t.Fatalf("rc %d local present %v old present %v:\n%s", rc, u.f.Has(pIDLocal), u.f.Has(pIDOld), out)
	}
	// The keep rules still hold under -prune-local.
	for _, id := range []string{pIDNew, pIDStop, pIDOther, pIDForgn} {
		if !u.f.Has(id) {
			t.Errorf("%s was removed under -prune-local", id)
		}
	}
	if !strings.Contains(out, "removed old box image culiops/culibox:2026-09-01") || !strings.Contains(out, "freeing up to 7100000000 bytes") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestBoxSetupNoPruneKeepsEverything(t *testing.T) {
	u := newUpgradeStore(t)
	if rc, out := u.setup(t, cliArgs{NoPrune: true}); rc != 0 || !u.f.Has(pIDOld) || u.f.Count("image rm") != 0 || u.f.Count("image ls") != 0 {
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
	u.f.FailRm(pIDOld)
	rc, out := u.setup(t, cliArgs{})
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
	rc, out := u.setup(t, cliArgs{})
	if rc != 0 || !u.f.Has(pIDOld) || u.f.Count("image rm") != 0 {
		t.Fatalf("rc %d old present %v:\n%s", rc, u.f.Has(pIDOld), out)
	}
	if !strings.Contains(out, "WARN image: old box images not removed") || !strings.Contains(out, "config.json") {
		t.Fatalf("output:\n%s", out)
	}
}

// C1: with a pinned (dated) box_image, -rebuild builds a NEW dated tag and the
// config stays on the old pin. The image just built is the one that must survive.
func TestRebuildOnAPinnedInstanceKeepsTheImageItJustBuilt(t *testing.T) {
	for _, prune := range []cliArgs{{Rebuild: true}, {Rebuild: true, PruneLocal: true}} {
		h := newInstHome(t)
		f := dockerfake.New(t)
		const pin = "culiops/culibox:2026-10-01"
		f.Add(local(pIDOld, 4_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-10-01"}))
		f.BuildAdds(local(pIDNew, 4_200_000_000))
		var w bytes.Buffer
		cfg := config.Config{BoxImage: pin}
		prune.Cmd, prune.Sub = "box", "setup"
		rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, pin, prune, h.src(cfg), setupNow, "linux", &w)
		out := w.String()
		const built = "culiops/culibox:2026-10-18"
		if rc != 0 || !strings.Contains(out, "PASS image: built "+built) {
			t.Fatalf("%+v: rc %d:\n%s", prune, rc, out)
		}
		if !f.Has(pIDNew) || !f.Has(pIDOld) || strings.Contains(out, "removed old box image") || f.Count("image rm") != 0 {
			t.Fatalf("%+v: built present %v, pin present %v:\n%s\n%s", prune, f.Has(pIDNew), f.Has(pIDOld), out, f.Log())
		}
	}
}

// The default :base upgrade leak: a rebuild moves :base to the new image and the
// old one is left with the dated tag its own build gave it (a real build carries
// :base AND :<date>). It is the image this setup orphaned: removed, and the
// removal line says what that costs an instance pinned to that tag elsewhere.
func TestRebuildOfBaseRemovesTheImageItOrphanedAndSaysWhatThatCosts(t *testing.T) {
	h := newInstHome(t)
	f := dockerfake.New(t)
	f.Add(local(pIDOld, 4_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "base"}, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-10-01"}))
	// An older leftover that this setup did not orphan: listed, not removed.
	f.Add(local(pIDLocal, 3_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-09-01"}))
	f.BuildAdds(local(pIDNew, 4_200_000_000))
	var w bytes.Buffer
	cfg := config.Config{}
	rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Cmd: "box", Sub: "setup", Rebuild: true}, h.src(cfg), setupNow, "linux", &w)
	out := w.String()
	if rc != 0 || f.Has(pIDOld) || !f.Has(pIDNew) || !f.Has(pIDLocal) {
		t.Fatalf("rc %d, old present %v, new %v, unrelated local %v:\n%s", rc, f.Has(pIDOld), f.Has(pIDNew), f.Has(pIDLocal), out)
	}
	for _, want := range []string{"removed old box image culiops/culibox:2026-10-01 (4000000000 bytes",
		"this is the image this setup replaced", "pinned to culiops/culibox:2026-10-01", "CULIBOX_HOME or -config",
		"`culibox box setup -rebuild`", "box_image updated", "-no-prune keeps it", "culiops/culibox:2026-09-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// An orphan with no name left cannot be pinned by anyone: no warning to give.
func TestRebuildOfBaseRemovesAnUnnamedOrphanWithoutTheWarning(t *testing.T) {
	h := newInstHome(t)
	f := dockerfake.New(t)
	f.Add(local(pIDOld, 4_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "base"}))
	f.BuildAdds(local(pIDNew, 4_200_000_000))
	var w bytes.Buffer
	cfg := config.Config{}
	boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Cmd: "box", Sub: "setup", Rebuild: true}, h.src(cfg), setupNow, "linux", &w)
	out := w.String()
	if f.Has(pIDOld) || !strings.Contains(out, "removed old box image "+strings.TrimPrefix(pIDOld, "sha256:")[:12]) ||
		strings.Contains(out, "this is the image this setup replaced") {
		t.Fatalf("old present %v:\n%s", f.Has(pIDOld), out)
	}
}

// ...but not when a stopped container uses it.
func TestRebuildOfBaseKeepsAnOrphanAContainerUses(t *testing.T) {
	h := newInstHome(t)
	f := dockerfake.New(t)
	f.Add(local(pIDOld, 4_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "base"}))
	f.Container("stopped-box", pIDOld)
	f.BuildAdds(local(pIDNew, 4_200_000_000))
	var w bytes.Buffer
	cfg := config.Config{}
	rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Cmd: "box", Sub: "setup", Rebuild: true}, h.src(cfg), setupNow, "linux", &w)
	if rc != 0 || !f.Has(pIDOld) || f.Count("image rm") != 0 {
		t.Fatalf("rc %d:\n%s\n%s", rc, w.String(), f.Log())
	}
}

// The containerd image store shows a digest on local builds too. A backup that
// pins a dated tag still keeps its image, and neither digest makes a local build
// look re-pullable.
func TestContainerdStoreLocalBuildsAreNeitherRemovedNorMistakenForPullable(t *testing.T) {
	// Containerd-style rows: a digest on a Docker Hub name.
	h := newInstHome(t)
	f := dockerfake.New(t)
	f.PullAdds(ghcr+"@"+pDigNew, pulled(pIDNew, 4_200_000_000, pDigNew))
	const idBackup = "sha256:1212000000000000000000000000000000000000000000000000000000001212"
	f.Add(local(idBackup, 4_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-08-01", Digest: pDigOld}))
	f.Add(local(pIDLocal, 3_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-09-01", Digest: pDigOthr}))
	writeFile(t, filepath.Join(h.root, "backups", "0.1-to-0.2-20261101T000000Z", "config.json"), cfgJSON("culiops/culibox:2026-08-01"))
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@" + pDigNew}
	for _, a := range []cliArgs{{}, {PruneLocal: true}} {
		w.Reset()
		a.Cmd, a.Sub = "box", "setup"
		rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(), a, h.src(cfg), setupNow, "linux", &w)
		out := w.String()
		if rc != 0 || !f.Has(idBackup) {
			t.Fatalf("%+v: rc %d, the backup's image present %v:\n%s", a, rc, f.Has(idBackup), out)
		}
		if !a.PruneLocal && (!f.Has(pIDLocal) || !strings.Contains(out, "kept culiops/culibox:2026-08-01 (4.0 GB): a saved upgrade backup's config pins it")) {
			t.Fatalf("default: local present %v:\n%s", f.Has(pIDLocal), out)
		}
		if a.PruneLocal && f.Has(pIDLocal) {
			t.Fatalf("-prune-local left the unpinned local build:\n%s", out)
		}
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
	f.Add(local(pIDOld, 1, dockerfake.Ref{Repo: repoOf(config.DefaultBoxImage), Tag: defTag}))
	f.Add(local(pIDStop, 1, dockerfake.Ref{Repo: repoOf(config.LegacyDefaultBoxImage), Tag: legTag}))
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@" + pDigNew}
	if rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(),
		cliArgs{Sub: "setup", PruneLocal: true}, h.src(cfg), setupNow, "linux", &w); rc != 0 {
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

func TestParsePruneFlags(t *testing.T) {
	a, err := parseArgs([]string{"box", "setup", "-no-prune"})
	if err != nil || !a.NoPrune || a.PruneLocal {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = parseArgs([]string{"box", "setup", "-prune-local"})
	if err != nil || a.NoPrune || !a.PruneLocal {
		t.Fatalf("%+v %v", a, err)
	}
	if a, err = parseArgs([]string{"box", "setup"}); err != nil || a.NoPrune || a.PruneLocal {
		t.Fatalf("%+v %v", a, err)
	}
	for _, argv := range [][]string{{"box", "status", "-no-prune"}, {"doctor", "-no-prune"}, {"box", "status", "-prune-local"},
		{"box", "setup", "-no-prune", "-prune-local"}} {
		if _, err = parseArgs(argv); err == nil {
			t.Errorf("%v accepted", argv)
		}
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

// On macOS the docker commands printed carry the box VM's --host, so they reach
// the daemon that holds the images; -prune-local is said to remove ALL listed.
func TestPrintedDockerCommandsCarryTheVMDaemonFlags(t *testing.T) {
	u := newUpgradeStore(t)
	// A rollback image and an image held by the pre-rename default name.
	writeFile(t, filepath.Join(u.h.root, "backups", "0.1-to-0.2-20261101T000000Z", "config.json"), cfgJSON("culiops/culibox:2026-08-01"))
	const idBackup = "sha256:1212000000000000000000000000000000000000000000000000000000001212"
	const idLegacy = "sha256:3434000000000000000000000000000000000000000000000000000000003434"
	u.f.Add(local(idBackup, 2_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-08-01"}))
	u.f.Add(local(idLegacy, 1_000_000_000, dockerfake.Ref{Repo: "culiops/agent-box", Tag: "base"}))
	var w bytes.Buffer
	cfg := config.Config{BoxImage: ghcr + "@" + pDigNew}
	d := boximage.Docker{Bin: u.f.Bin, Args: []string{"--host", "unix:///vm/docker.sock"}}
	boxSetupAndPrune(context.Background(), d, cfg.BoxImageOrDefault(), cliArgs{Cmd: "box", Sub: "setup"}, u.h.src(cfg), setupNow, "darwin", &w)
	out := w.String()
	for _, want := range []string{
		"culiops/culibox:2026-09-01 (3.0 GB): `docker --host unix:///vm/docker.sock image rm " + pIDLocal + "`",
		"removes ALL of the images listed above",
		"kept culiops/culibox:2026-08-01 (2.0 GB): a saved upgrade backup's config pins it",
		"`docker --host unix:///vm/docker.sock image rm " + idBackup + "`",
		// M6: held by a default name, said instead of silent.
		"kept culiops/agent-box:base (1.0 GB): it holds a name", "`docker --host unix:///vm/docker.sock image rm " + idLegacy + "`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A box_image another config names that is not a valid image reference names no
// image: it is skipped, with a WARN, and the prune goes on.
func TestPruneSkipsAnInvalidPinWithAWarning(t *testing.T) {
	u := newUpgradeStore(t)
	writeFile(t, filepath.Join(u.h.xdg, "agent-box", "config.json"), cfgJSON("-bad image"))
	writeFile(t, filepath.Join(u.h.root, "backups", "0.1-to-0.2-20261101T000000Z", "config.json"), cfgJSON("also bad"))
	rc, out := u.setup(t, cliArgs{})
	if rc != 0 || u.f.Has(pIDOld) {
		t.Fatalf("rc %d, old present %v:\n%s", rc, u.f.Has(pIDOld), out)
	}
	if strings.Count(out, "WARN image: ignoring a box_image that is not a valid image reference") != 2 ||
		!strings.Contains(out, filepath.Join(u.h.xdg, "agent-box", "config.json")) {
		t.Fatalf("output:\n%s", out)
	}
}

// A removal by several names that fails part-way has already untagged some of
// them: the WARN says which and prints the line that puts each back.
func TestPartialUntagPrintsTheRestoreLine(t *testing.T) {
	u := newUpgradeStore(t)
	const idMulti = "sha256:5656000000000000000000000000000000000000000000000000000000005656"
	u.f.Add(local(idMulti, 1_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-07-01"}, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-07-02"}))
	u.f.FailRm("culiops/culibox:2026-07-02")
	rc, out := u.setup(t, cliArgs{PruneLocal: true})
	if rc != 0 {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if !strings.Contains(out, "WARN image: could not remove old box image culiops/culibox:2026-07-01, culiops/culibox:2026-07-02") ||
		!strings.Contains(out, "culiops/culibox:2026-07-01 was already taken off it; put it back with: `docker tag "+idMulti+" culiops/culibox:2026-07-01`") {
		t.Fatalf("output:\n%s", out)
	}
}

// The printed command is pasted into a shell: a data root with a space or a quote
// in its path (CULIBOX_HOME="/Volumes/My Drive/it's") must come out quoted.
func TestDockerLineQuotesEveryArgument(t *testing.T) {
	d := boximage.Docker{Bin: "docker", Args: []string{"--config", "/Volumes/My Drive/it's/docker", "--host", "unix:///Volumes/My Drive/it's/d.sock"}}
	got := dockerLine(d, "image", "rm", "culiops/culibox:2026-10-01", "a;b $(x)")
	want := "`docker --config '/Volumes/My Drive/it'\\''s/docker' --host 'unix:///Volumes/My Drive/it'\\''s/d.sock' image rm culiops/culibox:2026-10-01 'a;b $(x)'`"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := dockerLine(boximage.Docker{}, "tag", pIDOld, "culiops/culibox:x"); got != "`docker tag "+pIDOld+" culiops/culibox:x`" {
		t.Fatalf("plain args were quoted: %s", got)
	}
}

// #335: an upgraded release binary with no box_image pulls its published image,
// names it culiops/culibox:base, and the prune then removes the stale local build
// :base held before (the orphan) and an older published image left only under its
// ghcr name (re-pullable). The pulled default is kept, and a second setup changes
// nothing.
func TestBoxSetupPullsThePublishedDefaultAndPrunesWhatItReplaced(t *testing.T) {
	old := publishedBoxImage
	t.Cleanup(func() { publishedBoxImage = old })
	ref := ghcr + "@" + pDigNew
	publishedBoxImage = func() string { return ref }

	h := newInstHome(t)
	f := dockerfake.New(t)
	stale := local(pIDOld, 4_000_000_000, dockerfake.Ref{Repo: "culiops/culibox", Tag: "base"}, dockerfake.Ref{Repo: "culiops/culibox", Tag: "2026-10-01"})
	stale.Hash = "000000000000"
	f.Add(stale)
	f.Add(pulled(pIDOther, 3_900_000_000, pDigOthr)) // an older published image, ghcr name only
	f.PullAdds(ref, pulled(pIDNew, 4_200_000_000, pDigNew))
	f.Add(dockerfake.Image{ID: pIDForgn, Size: 900_000_000, Refs: []dockerfake.Ref{{Repo: "postgres", Tag: "16"}}})

	setup := func() (int, string) {
		var w bytes.Buffer
		cfg := config.Config{}
		rc := boxSetupAndPrune(context.Background(), boximage.Docker{Bin: f.Bin}, cfg.BoxImageOrDefault(),
			cliArgs{Cmd: "box", Sub: "setup"}, h.src(cfg), setupNow, "linux", &w)
		return rc, w.String()
	}
	rc, out := setup()
	if rc != 0 {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if f.Count("build") != 0 || f.Count("pull") != 1 {
		t.Fatalf("want one pull and no build:\n%s", f.Log())
	}
	if !f.Has(pIDNew) || f.Has(pIDOld) || f.Has(pIDOther) || !f.Has(pIDForgn) {
		t.Fatalf("new %v, old :base %v, old published %v, foreign %v:\n%s",
			f.Has(pIDNew), f.Has(pIDOld), f.Has(pIDOther), f.Has(pIDForgn), out)
	}
	for _, want := range []string{"PASS image: pulled " + ref, "PASS image: culiops/culibox:base now points at " + ref,
		"removed old box image culiops/culibox:2026-10-01", "this is the image this setup replaced",
		"removed old box image " + ghcr + "@" + pDigOthr} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// The default now resolves to the pulled image: current, nothing to do.
	rc, out = setup()
	if rc != 0 || !strings.Contains(out, "culiops/culibox:base is current") || f.Count("pull") != 1 || !f.Has(pIDNew) {
		t.Fatalf("second setup: rc %d, pulls %d, new present %v:\n%s", rc, f.Count("pull"), f.Has(pIDNew), out)
	}
}
