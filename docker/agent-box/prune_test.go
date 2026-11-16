package boximage

import (
	"context"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/dockerfake"
)

const (
	idNew     = "sha256:aaaa0000000000000000000000000000000000000000000000000000000000aa"
	idOld     = "sha256:bbbb0000000000000000000000000000000000000000000000000000000000bb"
	idOlder   = "sha256:cccc0000000000000000000000000000000000000000000000000000000000cc"
	idOther   = "sha256:dddd0000000000000000000000000000000000000000000000000000000000dd"
	digestNew = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestOld = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	digestOth = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	ghcrBox   = "ghcr.io/culiops/culibox"
)

func ref(repo, tag, digest string) dockerfake.Ref {
	return dockerfake.Ref{Repo: repo, Tag: tag, Digest: digest}
}

// boxImg makes a labeled box image of the given size under the given names.
func boxImg(id string, size int64, refs ...dockerfake.Ref) dockerfake.Image {
	return dockerfake.Image{ID: id, Size: size, Labeled: true, Refs: refs}
}

// pulledImg is an image CI published, as `box setup` pulls it: by digest.
func pulledImg(id string, size int64, digest string) dockerfake.Image {
	return boxImg(id, size, ref(ghcrBox, "", digest))
}

// A store with the new image (kept) and nothing else; tests add what they probe.
func newStore(t *testing.T) (*dockerfake.Fake, PruneInput) {
	t.Helper()
	f := dockerfake.New(t)
	f.Add(pulledImg(idNew, 4_200_000_000, digestNew))
	return f, PruneInput{Keep: []string{ghcrBox + "@" + digestNew}}
}

func prune(t *testing.T, f *dockerfake.Fake, in PruneInput) (PruneResult, error) {
	t.Helper()
	return Docker{Bin: f.Bin}.PruneOld(context.Background(), in)
}

func names(imgs []StoreImage) []string {
	var out []string
	for _, im := range imgs {
		out = append(out, im.Name())
	}
	return out
}

func noRm(t *testing.T, f *dockerfake.Fake, what string) {
	t.Helper()
	if f.Count("image rm") != 0 {
		t.Fatalf("%s, but docker was asked to remove something:\n%s", what, f.Log())
	}
}

// The upgrade case: a new digest was pulled, the previous one can be pulled again.
func TestPruneRemovesAnOldRePullableImageAndKeepsTheNewOne(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 4_100_000_000, digestOld))
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != ghcrBox+"@"+digestOld {
		t.Fatalf("removed %v", got)
	}
	if res.Removed[0].Size != 4_100_000_000 || res.FreedBytes() != 4_100_000_000 {
		t.Fatalf("size %d freed %d", res.Removed[0].Size, res.FreedBytes())
	}
	if f.Has(idOld) || !f.Has(idNew) {
		t.Fatalf("store after: old present %v, new present %v", f.Has(idOld), f.Has(idNew))
	}
}

// The keep rule is on the image, under every name, and for every image setup made.
func TestPruneKeepsEveryImageTheSetupMade(t *testing.T) {
	f := dockerfake.New(t)
	// A pinned config and a -rebuild: the config's image and the new dated tag.
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "2026-11-16", ""), ref("culiops/culibox", "2026-11-16.2", "")))
	f.Add(boxImg(idOld, 1, ref("culiops/culibox", "2026-10-01", "")))
	res, err := prune(t, f, PruneInput{Keep: []string{"culiops/culibox:2026-10-01", "culiops/culibox:2026-11-16.2"}, Local: true})
	if err != nil || len(res.Removed) != 0 || !f.Has(idNew) || !f.Has(idOld) {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestPruneKeepsAnImageAnyContainerUses(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 1, digestOld))
	f.Add(pulledImg(idOlder, 1, digestOth))
	f.Container("c-running", idOld) // the fake does not distinguish states: ls -a lists all
	f.Container("c-stopped", idOlder)
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 0 || !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatalf("removed %v failed %v", names(res.Removed), res.Failed)
	}
	noRm(t, f, "containers use both")
	// ps -a, not ps: a stopped container pins its image.
	if !strings.Contains(f.Log(), "container ls -a") {
		t.Fatalf("containers listed without -a:\n%s", f.Log())
	}
}

func TestPruneKeepsAnImageAConfigPins(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 1, digestOld))
	f.Add(boxImg(idOlder, 1, ref("culiops/culibox", "2026-10-01", "")))
	f.Add(boxImg(idOther, 1, ref("culiops/culibox", "2026-08-01", "")))
	in.Local = true
	in.Pins = []string{ghcrBox + "@" + digestOld, "culiops/culibox:2026-10-01", "culiops/culibox:2026-01-01"}
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != "culiops/culibox:2026-08-01" {
		t.Fatalf("removed %v, want only the unpinned 2026-08-01", got)
	}
	if !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatal("a pinned image was removed")
	}
}

func TestPruneNeverTouchesAnImageThatIsNotAgentBox(t *testing.T) {
	f, in := newStore(t)
	in.Local = true
	const idShared = "sha256:eeee0000000000000000000000000000000000000000000000000000000000ee"
	const idDangling = "sha256:ffff0000000000000000000000000000000000000000000000000000000000ff"
	const idDerived = "sha256:9999000000000000000000000000000000000000000000000000000000000099"
	// Right name, no box label: not built by us.
	f.Add(dockerfake.Image{ID: idOld, Size: 1, Refs: []dockerfake.Ref{ref("culiops/culibox", "2026-10-01", "")}})
	// Box label, another project's name: an image built FROM the box image inherits the label.
	f.Add(boxImg(idOlder, 1, ref("acme/webapp", "1.2", "")))
	f.Add(boxImg(idDerived, 1, ref("myorg/devbox", "1", "")))
	// Another project's image, no label.
	f.Add(dockerfake.Image{ID: idOther, Size: 1, Refs: []dockerfake.Ref{ref("postgres", "16", "")}})
	// A box image that someone also gave another project's name: not ours to untag.
	f.Add(boxImg(idShared, 1, ref("culiops/culibox", "2026-07-01", ""), ref("acme/webapp", "latest", "")))
	// A dangling labelled image that is not what this setup orphaned: maybe someone's derived image.
	f.Add(boxImg(idDangling, 1, ref("", "", "")))
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 0 || len(res.KeptLocal) != 0 {
		t.Fatalf("removed %v failed %v listed %v", names(res.Removed), res.Failed, names(res.KeptLocal))
	}
	for _, id := range []string{idOld, idOlder, idOther, idShared, idDangling, idDerived} {
		if !f.Has(id) {
			t.Errorf("%s was removed", id)
		}
	}
	noRm(t, f, "nothing here is a box image")
}

// Only BoxRepos count: the repository a visible config names does not widen them.
func TestPruneRecognisesEveryBoxRepositoryAndOnlyThose(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 10, digestOld))
	f.Add(boxImg(idOlder, 20, ref("ghcr.io/culiops/agent-box", "", digestOth)))
	f.Add(boxImg(idOther, 30, ref("registry.example.com/team/box", "", digestNew)))
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 2 || res.FreedBytes() != 30 || !f.Has(idOther) {
		t.Fatalf("removed %v freed %d", names(res.Removed), res.FreedBytes())
	}
}

// (b) The image this setup orphaned: the old :base after a rebuild or a new default.
func TestPruneRemovesTheImageThisSetupOrphaned(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", ""), ref("culiops/culibox", "2026-11-16", "")))
	f.Add(boxImg(idOld, 5, ref("", "", ""))) // base moved off it: dangling
	f.Add(boxImg(idOlder, 6, ref("", "", "")))
	res, err := prune(t, f, PruneInput{Keep: []string{"culiops/culibox:base"}, Orphan: idOld})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != idOld[len("sha256:"):len("sha256:")+12] {
		t.Fatalf("removed %v", got)
	}
	if f.Has(idOld) || !f.Has(idOlder) {
		t.Fatalf("orphan present %v, unrelated dangling present %v", f.Has(idOld), f.Has(idOlder))
	}
}

func TestPruneOrphanIsStillSubjectToTheKeepRules(t *testing.T) {
	cases := []struct {
		name string
		mk   func(f *dockerfake.Fake, in *PruneInput)
	}{
		{"a container uses it", func(f *dockerfake.Fake, in *PruneInput) { f.Container("c", idOld) }},
		{"a config pins it", func(f *dockerfake.Fake, in *PruneInput) { in.Pins = []string{"culiops/culibox:2026-10-01"} }},
		{"a backup would restore it", func(f *dockerfake.Fake, in *PruneInput) { in.RollbackPins = []string{"culiops/culibox:2026-10-01"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := dockerfake.New(t)
			f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
			f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))
			in := PruneInput{Keep: []string{"culiops/culibox:base"}, Orphan: idOld}
			c.mk(f, &in)
			res, err := prune(t, f, in)
			if err != nil || len(res.Removed) != 0 || !f.Has(idOld) {
				t.Fatalf("removed %v err %v", names(res.Removed), err)
			}
			noRm(t, f, "the orphan is kept")
		})
	}
}

// An orphan that is not a box image (a user's own repository) is not swept.
func TestPruneOrphanMustStillBeABoxImage(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("myorg/devbox", "1", "")))
	f.Add(dockerfake.Image{ID: idOlder, Size: 1, Refs: []dockerfake.Ref{ref("culiops/culibox", "x", "")}}) // no label
	for _, orphan := range []string{idOld, idOlder} {
		res, err := prune(t, f, PruneInput{Keep: []string{"culiops/culibox:base"}, Orphan: orphan})
		if err != nil || len(res.Removed) != 0 {
			t.Fatalf("orphan %s: removed %v err %v", orphan, names(res.Removed), err)
		}
	}
	noRm(t, f, "no orphan was a box image")
}

func TestOrphanOf(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idOld, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOlder, 1, ref("myorg/devbox", "1", "")))
	d := Docker{Bin: f.Bin}
	ctx := context.Background()
	if got := d.OrphanOf(ctx, "culiops/culibox:base"); got != idOld {
		t.Errorf("box name: %q", got)
	}
	if got := d.OrphanOf(ctx, "myorg/devbox:1"); got != "" {
		t.Errorf("a repository that is not a box repository: %q", got)
	}
	if got := d.OrphanOf(ctx, "culiops/culibox:2026-01-01"); got != "" {
		t.Errorf("a name the daemon does not hold: %q", got)
	}
	if got := (Docker{Bin: "/bin/false"}).OrphanOf(ctx, "culiops/culibox:base"); got != "" {
		t.Errorf("a daemon that does not answer: %q", got)
	}
}

// (c) An unused local build cannot be pulled again and an instance this process
// cannot see may pin it: it is listed, not removed, until Local is set.
func TestPruneListsUnusedLocalBuildsAndRemovesThemOnlyOnRequest(t *testing.T) {
	f, in := newStore(t)
	f.Add(boxImg(idOld, 7, ref("culiops/culibox", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 9, ref("culiops/agent-box", "2026-09-01", ""), ref("culiops/agent-box", "2026-09-02", "")))
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.KeptLocal) != 2 || res.KeptLocal[0].Size != 7 || res.KeptLocal[1].Size != 9 {
		t.Fatalf("removed %v listed %+v", names(res.Removed), res.KeptLocal)
	}
	noRm(t, f, "local builds are only listed")
	in.Local = true
	res, err = prune(t, f, in)
	if err != nil || len(res.Removed) != 2 || len(res.KeptLocal) != 0 || res.FreedBytes() != 16 {
		t.Fatalf("removed %v listed %v err %v", names(res.Removed), names(res.KeptLocal), err)
	}
	if f.Has(idOld) || f.Has(idOlder) || !f.Has(idNew) {
		t.Fatal("store after is wrong")
	}
}

// The containerd image store shows a digest on every image, local builds too: a
// digest on a Docker Hub style name is not evidence a registry has it.
func TestPruneDoesNotReadADigestOnALocalNameAsRePullable(t *testing.T) {
	f, in := newStore(t)
	f.Add(boxImg(idOld, 7, ref("culiops/culibox", "2026-10-01", digestOld)))
	// Mixed: a registry name that also carries a local tag is not "only ghcr".
	f.Add(boxImg(idOlder, 9, ref(ghcrBox, "", digestOth), ref("culiops/culibox", "mine", "")))
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.KeptLocal) != 2 || !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatalf("removed %v listed %v", names(res.Removed), names(res.KeptLocal))
	}
	noRm(t, f, "neither can be shown to be pullable")
}

// A pulled-by-tag ghcr image (digest recorded on its tag row) is still re-pullable.
func TestPruneRemovesAGhcrImageNamedByTag(t *testing.T) {
	f, in := newStore(t)
	f.Add(boxImg(idOld, 7, ref(ghcrBox, "base", digestOld)))
	res, err := prune(t, f, in)
	if err != nil || len(res.Removed) != 1 || f.Has(idOld) {
		t.Fatalf("removed %v err %v", names(res.Removed), err)
	}
}

// One image under two names: removed with both names when nothing pins it.
func TestPruneHandlesAnImageWithSeveralNames(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 7, ref("culiops/culibox", "2026-10-01", ""), ref("culiops/culibox", "2026-10-02", "")))
	f.Add(boxImg(idOlder, 9, ref("culiops/culibox", "2026-09-01", ""), ref("culiops/agent-box", "base", "")))
	in := PruneInput{Keep: []string{"culiops/culibox:base"}, Local: true, Pins: []string{"culiops/culibox:2026-10-02"}}
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	// Pinning either name keeps the image; the legacy default tag is never removed.
	if len(res.Removed) != 0 || !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatalf("removed %v", names(res.Removed))
	}
	in.Pins = nil
	res, err = prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != "culiops/culibox:2026-10-01, culiops/culibox:2026-10-02" {
		t.Fatalf("removed %v failed %v", got, res.Failed)
	}
	if f.Has(idOld) || !f.Has(idOlder) {
		t.Fatal("store after is wrong")
	}
}

// A single-name image goes by its ID, so no untag can happen without the delete.
func TestPruneRemovesBySingleNameImagesByID(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 1, digestOld))
	if _, err := prune(t, f, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Log(), "image rm "+idOld) {
		t.Fatalf("not removed by ID:\n%s", f.Log())
	}
}

// A failure to remove one image is reported and the rest still go.
func TestPruneReportsAFailedRemovalAndContinues(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 5, digestOld))
	f.Add(pulledImg(idOlder, 6, digestOth))
	f.FailRm(idOld)
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || res.Failed[0].Image.ID != idOld || !strings.Contains(res.Failed[0].Err.Error(), "simulated failure") {
		t.Fatalf("failed %+v", res.Failed)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != ghcrBox+"@"+digestOth || res.FreedBytes() != 6 {
		t.Fatalf("removed %v freed %d", got, res.FreedBytes())
	}
	if !f.Has(idOld) || f.Has(idOlder) {
		t.Fatal("store after is wrong")
	}
}

// An rm that says yes while the image stays is not "freed".
func TestPruneDoesNotCountAnImageThatIsStillThere(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 5, digestOld))
	f.RmNoop(idOld)
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || res.FreedBytes() != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Err.Error(), "still in the store") {
		t.Fatalf("removed %v failed %+v", names(res.Removed), res.Failed)
	}
}

// docker is the last line of defence against removing an image in use, so the
// code must never force it, whatever the plan said.
func TestPruneNeverForcesARemoval(t *testing.T) {
	f, in := newStore(t)
	in.Local = true
	f.Add(pulledImg(idOld, 5, digestOld))
	f.Add(boxImg(idOlder, 5, ref("culiops/culibox", "2026-10-01", ""), ref("culiops/culibox", "2026-10-02", "")))
	if _, err := prune(t, f, in); err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(f.Log(), "\n") {
		if strings.Contains(l, " -f") || strings.Contains(l, "--force") {
			t.Fatalf("forced: %q", l)
		}
	}
	if f.Count("image rm") == 0 {
		t.Fatal("nothing was removed; the test proves nothing")
	}
}

// A container that appears between the plan and the removal keeps its image.
func TestPruneReadsContainersAgainBeforeRemoving(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 5, digestOld))
	in.beforeRemove = func() { f.Container("late", idOld) }
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 1 || !f.Has(idOld) {
		t.Fatalf("removed %v failed %+v", names(res.Removed), res.Failed)
	}
	noRm(t, f, "the container appeared before the removal")
}

// The daemon is listed before the pins are resolved: a tag that moves in between
// cannot make a listed image look unpinned.
func TestPruneListsBeforeItResolvesPins(t *testing.T) {
	f, in := newStore(t)
	f.Add(pulledImg(idOld, 5, digestOld))
	if _, err := prune(t, f, in); err != nil {
		t.Fatal(err)
	}
	log := f.Log()
	ls, inspect := strings.Index(log, "image ls"), strings.Index(log, "image inspect")
	if ls < 0 || inspect < 0 || ls > inspect {
		t.Fatalf("image ls at %d, first image inspect at %d:\n%s", ls, inspect, log)
	}
}

// A backup's pin that no registry can supply again (a local build, which only
// ever exists under a dated tag or :base) is kept; the caller decides which pins
// those are.
func TestPruneKeepsARollbackImage(t *testing.T) {
	f, in := newStore(t)
	f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))
	// The containerd store shows a digest on a local build: still kept.
	f.Add(boxImg(idOlder, 6, ref("culiops/culibox", "2026-09-01", digestOld)))
	f.Add(boxImg(idOther, 7, ref("culiops/culibox", "2026-08-01", ""))) // pinned by nobody
	in.RollbackPins = []string{"culiops/culibox:2026-10-01", "culiops/culibox:2026-09-01"}
	in.Local = true
	res, err := prune(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != "culiops/culibox:2026-08-01" {
		t.Fatalf("removed %v", got)
	}
	if !f.Has(idOld) || !f.Has(idOlder) || len(res.KeptForRollback) != 2 {
		t.Fatalf("kept for rollback %v", names(res.KeptForRollback))
	}
}

// Anything docker cannot answer fails the prune before it removes anything: an
// unknown is never read as "unused".
func TestPruneFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		in   PruneInput
	}{
		{"the just-set-up image is not there", PruneInput{Keep: []string{"culiops/culibox:nope"}}},
		{"nothing is named as just set up", PruneInput{}},
		{"a pin that is not a valid reference", PruneInput{Keep: []string{ghcrBox + "@" + digestNew}, Pins: []string{"bad ref"}}},
		{"a rollback pin that is not a valid reference", PruneInput{Keep: []string{ghcrBox + "@" + digestNew}, RollbackPins: []string{"bad ref"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := newStore(t)
			f.Add(pulledImg(idOld, 5, digestOld))
			res, err := prune(t, f, c.in)
			if err == nil {
				t.Fatalf("no error; removed %v", names(res.Removed))
			}
			if len(res.Removed) != 0 || !f.Has(idOld) {
				t.Fatalf("removed %v\n%s", names(res.Removed), f.Log())
			}
			noRm(t, f, "the prune failed closed")
		})
	}
}

func TestPruneFailsClosedWhenDockerAnswersNothing(t *testing.T) {
	res, err := Docker{Bin: "/bin/false"}.PruneOld(context.Background(), PruneInput{Keep: []string{"culiops/culibox:base"}})
	if err == nil || len(res.Removed) != 0 {
		t.Fatalf("err %v removed %v", err, names(res.Removed))
	}
}

func TestPruneIgnoresATagOnlyMatchOfAnotherRepository(t *testing.T) {
	// A pin spelled for one repository must not protect (or expose) a same-tag
	// image in another one: resolution is by the daemon, not by tag text.
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("culiops/agent-box", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 5, ref("culiops/culibox", "2026-10-01", "")))
	res, err := prune(t, f, PruneInput{Keep: []string{"culiops/culibox:base"}, Local: true, Pins: []string{"culiops/culibox:2026-10-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Removed); len(got) != 1 || got[0] != "culiops/agent-box:2026-10-01" {
		t.Fatalf("removed %v", got)
	}
}

func TestStoreImageNameAndRmArgs(t *testing.T) {
	im := StoreImage{ID: "sha256:0123456789abcdef0123456789abcdef", Refs: []string{"a:b", "c@sha256:x"}}
	if im.Name() != "a:b, c@sha256:x" {
		t.Fatal(im.Name())
	}
	if got := im.RmArgs(); len(got) != 2 || got[0] != "a:b" {
		t.Fatalf("several names: %v", got)
	}
	if got := (StoreImage{ID: "sha256:0123456789abcdef0123456789abcdef"}).Name(); got != "0123456789ab" {
		t.Fatalf("dangling name %q", got)
	}
	one := StoreImage{ID: "sha256:abc", Refs: []string{"a:b"}}
	if got := one.RmArgs(); len(got) != 1 || got[0] != "sha256:abc" {
		t.Fatalf("one name: %v", got)
	}
}
