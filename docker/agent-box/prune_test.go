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
)

func ref(repo, tag, digest string) dockerfake.Ref {
	return dockerfake.Ref{Repo: repo, Tag: tag, Digest: digest}
}

// boxImg makes a labeled box image of the given size under the given names.
func boxImg(id string, size int64, refs ...dockerfake.Ref) dockerfake.Image {
	return dockerfake.Image{ID: id, Size: size, Labeled: true, Refs: refs}
}

func prune(t *testing.T, f *dockerfake.Fake, in PruneInput) (PruneResult, error) {
	t.Helper()
	return Docker{Bin: f.Bin}.PruneOld(context.Background(), in)
}

func removedNames(r PruneResult) []string {
	var out []string
	for _, im := range r.Removed {
		out = append(out, im.Name())
	}
	return out
}

// The upgrade case: a new digest was pulled, the previous one is nobody's.
func TestPruneRemovesAnOldUnreferencedImageAndKeepsTheNewOne(t *testing.T) {
	f := dockerfake.New(t)
	newRef := "ghcr.io/culiops/culibox@" + digestNew
	f.Add(boxImg(idNew, 4_200_000_000, ref("ghcr.io/culiops/culibox", "", digestNew)))
	f.Add(boxImg(idOld, 4_100_000_000, ref("ghcr.io/culiops/culibox", "", digestOld)))
	res, err := prune(t, f, PruneInput{Keep: newRef, Pins: []string{newRef}})
	if err != nil {
		t.Fatal(err)
	}
	if got := removedNames(res); len(got) != 1 || got[0] != "ghcr.io/culiops/culibox@"+digestOld {
		t.Fatalf("removed %v", got)
	}
	if res.Removed[0].Size != 4_100_000_000 || res.FreedBytes() != 4_100_000_000 {
		t.Fatalf("size %d freed %d", res.Removed[0].Size, res.FreedBytes())
	}
	if f.Has(idOld) || !f.Has(idNew) {
		t.Fatalf("store after: old present %v, new present %v", f.Has(idOld), f.Has(idNew))
	}
}

func TestPruneNeverRemovesTheImageJustSetUpEvenIfNothingPinsIt(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "2026-11-16", "")))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:2026-11-16"})
	if err != nil || len(res.Removed) != 0 || !f.Has(idNew) {
		t.Fatalf("res %+v err %v present %v", res, err, f.Has(idNew))
	}
}

// Another name of the image just set up keeps it too: the keep rule is on the
// image, not on the one spelling box_image used.
func TestPruneKeepsTheJustSetUpImageUnderItsOtherNames(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", ""), ref("culiops/culibox", "2026-11-16", "")))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base"})
	if err != nil || len(res.Removed) != 0 || !f.Has(idNew) {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestPruneKeepsAnImageAnyContainerUses(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 1, ref("culiops/culibox", "2026-10-01", "")))
	f.Container("c-running", idOld) // the fake does not distinguish states: ls -a lists all
	f.Container("c-stopped", idOlder)
	f.Add(boxImg(idOlder, 1, ref("culiops/culibox", "2026-09-01", "")))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 0 || f.Count("image rm") != 0 || !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatalf("removed %v failed %v\n%s", removedNames(res), res.Failed, f.Log())
	}
	// ps -a, not ps: a stopped container pins its image.
	if !strings.Contains(f.Log(), "container ls -a") {
		t.Fatalf("containers listed without -a:\n%s", f.Log())
	}
}

func TestPruneKeepsAnImageAConfigPins(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 1, ref("culiops/culibox", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 1, ref("ghcr.io/culiops/culibox", "", digestOld)))
	f.Add(boxImg(idOther, 1, ref("culiops/culibox", "2026-08-01", "")))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base",
		Pins: []string{"culiops/culibox:2026-10-01", "ghcr.io/culiops/culibox@" + digestOld, "culiops/culibox:2026-01-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := removedNames(res); len(got) != 1 || got[0] != "culiops/culibox:2026-08-01" {
		t.Fatalf("removed %v, want only the unpinned 2026-08-01", got)
	}
	if !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatal("a pinned image was removed")
	}
}

func TestPruneNeverTouchesAnImageThatIsNotAgentBox(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	// Right name, no box label: not built by us.
	f.Add(dockerfake.Image{ID: idOld, Size: 1, Refs: []dockerfake.Ref{ref("culiops/culibox", "2026-10-01", "")}})
	// Box label, another project's name.
	f.Add(boxImg(idOlder, 1, ref("acme/webapp", "1.2", "")))
	// Another project's image, no label.
	f.Add(dockerfake.Image{ID: idOther, Size: 1, Refs: []dockerfake.Ref{ref("postgres", "16", "")}})
	// A box image that someone also gave another project's name: not ours to untag.
	const idShared = "sha256:eeee0000000000000000000000000000000000000000000000000000000000ee"
	f.Add(boxImg(idShared, 1, ref("culiops/culibox", "2026-07-01", ""), ref("acme/webapp", "latest", "")))
	// A dangling image with no box label.
	const idDangling = "sha256:ffff0000000000000000000000000000000000000000000000000000000000ff"
	f.Add(dockerfake.Image{ID: idDangling, Size: 1})
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 0 {
		t.Fatalf("removed %v failed %v", removedNames(res), res.Failed)
	}
	for _, id := range []string{idOld, idOlder, idOther, idShared, idDangling} {
		if !f.Has(id) {
			t.Errorf("%s was removed", id)
		}
	}
	if strings.Contains(f.Log(), "image rm") {
		t.Fatalf("docker was asked to remove something:\n%s", f.Log())
	}
}

// Every repository the code has ever used for the box image counts: the
// pre-rename culiops/agent-box (Docker Hub style and ghcr), and the repository
// of whatever a visible config pins.
func TestPruneRecognisesEveryBoxRepository(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 10, ref("culiops/agent-box", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 20, ref("ghcr.io/culiops/agent-box", "", digestOld)))
	f.Add(boxImg(idOther, 30, ref("registry.example.com/team/box", "", digestNew)))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base", Repos: []string{"registry.example.com/team/box"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 3 || res.FreedBytes() != 60 {
		t.Fatalf("removed %v freed %d", removedNames(res), res.FreedBytes())
	}
}

// A moved :base leaves the old image with no name at all; the box label is
// what says it is ours.
func TestPruneRemovesADanglingBoxImage(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("", "", ""))) // <none>:<none>
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base"})
	if err != nil {
		t.Fatal(err)
	}
	if got := removedNames(res); len(got) != 1 || got[0] != idOld[len("sha256:"):len("sha256:")+12] {
		t.Fatalf("removed %v", got)
	}
	if f.Has(idOld) {
		t.Fatal("dangling image still there")
	}
}

// One image under two names (the dated tag and :base a build makes): pinning
// either spelling keeps the image; with neither pinned both names go in one rm.
func TestPruneHandlesAnImageWithSeveralNames(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 7, ref("culiops/culibox", "2026-10-01", ""), ref("culiops/culibox", "2026-10-02", "")))
	f.Add(boxImg(idOlder, 9, ref("culiops/culibox", "2026-09-01", ""), ref("culiops/agent-box", "base", "")))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base", Pins: []string{"culiops/culibox:2026-10-02"}})
	if err != nil {
		t.Fatal(err)
	}
	// idOlder is held by the legacy default tag, a name a pre-rename binary runs:
	// that one is never removed either.
	if len(res.Removed) != 0 || !f.Has(idOld) || !f.Has(idOlder) {
		t.Fatalf("removed %v", removedNames(res))
	}
	res, err = prune(t, f, PruneInput{Keep: "culiops/culibox:base"})
	if err != nil {
		t.Fatal(err)
	}
	if got := removedNames(res); len(got) != 1 || got[0] != "culiops/culibox:2026-10-01, culiops/culibox:2026-10-02" {
		t.Fatalf("removed %v", got)
	}
	if f.Has(idOld) || !f.Has(idOlder) {
		t.Fatal("store after is wrong")
	}
}

// A failure to remove one image is reported and the rest still go.
func TestPruneReportsAFailedRemovalAndContinues(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 6, ref("culiops/culibox", "2026-09-01", "")))
	f.FailRm("culiops/culibox:2026-10-01")
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || res.Failed[0].Image.Name() != "culiops/culibox:2026-10-01" ||
		!strings.Contains(res.Failed[0].Err.Error(), "simulated failure") {
		t.Fatalf("failed %+v", res.Failed)
	}
	if got := removedNames(res); len(got) != 1 || got[0] != "culiops/culibox:2026-09-01" || res.FreedBytes() != 6 {
		t.Fatalf("removed %v freed %d", got, res.FreedBytes())
	}
	if !f.Has(idOld) || f.Has(idOlder) {
		t.Fatal("store after is wrong")
	}
}

// docker is the last line of defence against removing an image in use, so the
// code must never force it, whatever the plan said.
func TestPruneNeverForcesARemoval(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 5, ref("", "", "")))
	if _, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base"}); err != nil {
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

// When a container appears after the listing, docker's own refusal comes back
// as a failed removal, not a crash and not a forced removal.
func TestPruneTurnsDockersRefusalIntoAFailure(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))
	// The container shows up between the plan and the removal.
	res, err := Docker{Bin: f.Bin}.PruneOld(context.Background(), PruneInput{Keep: "culiops/culibox:base",
		beforeRemove: func() { f.Container("late", idOld) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Failed) != 1 || !f.Has(idOld) {
		t.Fatalf("removed %v failed %+v", removedNames(res), res.Failed)
	}
}

// What a restored upgrade backup would pin: kept when the image cannot be
// pulled again (a local build), let go when a registry has it.
func TestPruneKeepsARollbackImageOnlyWhenItCannotBePulledAgain(t *testing.T) {
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))        // local build
	f.Add(boxImg(idOlder, 6, ref("ghcr.io/culiops/culibox", "", digestOld))) // pulled by digest
	f.Add(boxImg(idOther, 7, ref("culiops/culibox", "2026-08-01", "")))      // pinned by nobody
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base",
		RollbackPins: []string{"culiops/culibox:2026-10-01", "ghcr.io/culiops/culibox@" + digestOld}})
	if err != nil {
		t.Fatal(err)
	}
	got := removedNames(res)
	if len(got) != 2 {
		t.Fatalf("removed %v", got)
	}
	if !f.Has(idOld) || f.Has(idOlder) || f.Has(idOther) {
		t.Fatalf("store after: local build kept=%v pulled kept=%v unpinned kept=%v", f.Has(idOld), f.Has(idOlder), f.Has(idOther))
	}
	if len(res.KeptForRollback) != 1 || res.KeptForRollback[0].Name() != "culiops/culibox:2026-10-01" {
		t.Fatalf("kept for rollback: %+v", res.KeptForRollback)
	}
}

// Anything docker cannot answer fails the prune before it removes anything: an
// unknown is never read as "unused".
func TestPruneFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		in   PruneInput
	}{
		{"the just-set-up image is not there", PruneInput{Keep: "culiops/culibox:nope"}},
		{"a pin that is not a valid reference", PruneInput{Keep: "culiops/culibox:base", Pins: []string{"bad ref"}}},
		{"a rollback pin that is not a valid reference", PruneInput{Keep: "culiops/culibox:base", RollbackPins: []string{"bad ref"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := dockerfake.New(t)
			f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
			f.Add(boxImg(idOld, 5, ref("culiops/culibox", "2026-10-01", "")))
			res, err := prune(t, f, c.in)
			if err == nil {
				t.Fatalf("no error; removed %v", removedNames(res))
			}
			if len(res.Removed) != 0 || !f.Has(idOld) || f.Count("image rm") != 0 {
				t.Fatalf("removed %v\n%s", removedNames(res), f.Log())
			}
		})
	}
}

func TestPruneFailsClosedWhenDockerAnswersNothing(t *testing.T) {
	res, err := Docker{Bin: "/bin/false"}.PruneOld(context.Background(), PruneInput{Keep: "culiops/culibox:base"})
	if err == nil || len(res.Removed) != 0 {
		t.Fatalf("err %v removed %v", err, removedNames(res))
	}
}

func TestPruneIgnoresATagOnlyMatchOfAnotherRepository(t *testing.T) {
	// A pin spelled for one repository must not protect (or expose) a same-tag
	// image in another one: resolution is by the daemon, not by tag text.
	f := dockerfake.New(t)
	f.Add(boxImg(idNew, 1, ref("culiops/culibox", "base", "")))
	f.Add(boxImg(idOld, 5, ref("culiops/agent-box", "2026-10-01", "")))
	f.Add(boxImg(idOlder, 5, ref("culiops/culibox", "2026-10-01", "")))
	res, err := prune(t, f, PruneInput{Keep: "culiops/culibox:base", Pins: []string{"culiops/culibox:2026-10-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := removedNames(res); len(got) != 1 || got[0] != "culiops/agent-box:2026-10-01" {
		t.Fatalf("removed %v", got)
	}
}

func TestStoreImageName(t *testing.T) {
	im := StoreImage{ID: "sha256:0123456789abcdef0123456789abcdef", Refs: []string{"a:b", "c@sha256:x"}}
	if im.Name() != "a:b, c@sha256:x" {
		t.Fatal(im.Name())
	}
	if got := (StoreImage{ID: "sha256:0123456789abcdef0123456789abcdef"}).Name(); got != "0123456789ab" {
		t.Fatalf("dangling name %q", got)
	}
}
