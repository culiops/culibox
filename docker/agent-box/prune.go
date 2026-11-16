package boximage

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// BoxRepos are the repositories the code has ever named the box image under:
// the Docker Hub style default (culiops/culibox, and culiops/agent-box from
// before the rename, #255) and the ghcr package CI publishes to (and the one
// it had before the rename). The ghcr layer-cache package agent-box-cache is
// not here: it is never pulled or run.
var BoxRepos = []string{
	"culiops/culibox", "culiops/agent-box",
	"ghcr.io/culiops/culibox", "ghcr.io/culiops/agent-box",
}

// defaultPins are the names an instance runs when its config names no box_image,
// or no config exists at all: the default, and the pre-rename default that a
// not-yet-upgraded CuliBox (a rollback target) still runs. An image holding
// either name is never pruned, whatever configs are visible.
var defaultPins = []string{"culiops/culibox:base", "culiops/agent-box:base"}

// StoreImage is one image in the daemon's store, with every name it has.
type StoreImage struct {
	ID        string   // the full ID, sha256:…
	Refs      []string // repo:tag and repo@digest names; none = dangling
	Repos     []string // the distinct repositories behind Refs
	Labeled   bool     // carries LabelHash: built from CuliBox's Dockerfile
	HasDigest bool     // a registry digest is recorded: it was pulled, so a registry has it
	Size      int64    // bytes, from image inspect (filled for removal candidates)
}

// Name is how output names the image: its names, or a short ID when it has none.
func (i StoreImage) Name() string {
	if len(i.Refs) > 0 {
		return strings.Join(i.Refs, ", ")
	}
	short := strings.TrimPrefix(i.ID, "sha256:")
	return short[:min(12, len(short))]
}

// PruneInput is what PruneOld keeps, besides what no container-free, unpinned
// box image needs.
type PruneInput struct {
	// Keep is the image just set up. It is never removed (nor any other name of it).
	Keep string
	// Pins are the images a visible instance config names: never removed.
	Pins []string
	// RollbackPins are the images a retained upgrade backup's config names, which a
	// documented rollback restores. They are kept only when they cannot be pulled
	// again; a registry digest can be, so it is let go.
	RollbackPins []string
	// Repos are repositories, besides BoxRepos, that count as box images: those a
	// visible config's box_image names. An image there still needs LabelHash.
	Repos []string

	beforeRemove func() // test hook: runs after the plan, before the first removal
}

// PruneFailure is an image that was due for removal and could not be removed.
type PruneFailure struct {
	Image StoreImage
	Err   error
}

// PruneResult is what PruneOld did.
type PruneResult struct {
	Removed []StoreImage
	Failed  []PruneFailure
	// KeptForRollback are images kept only because a retained backup's config pins
	// them and no registry can supply them again.
	KeptForRollback []StoreImage
}

// FreedBytes is the sum of the removed images' sizes. It is an upper bound: an
// image's size counts layers a kept image may share, and those stay.
func (r PruneResult) FreedBytes() int64 {
	var n int64
	for _, im := range r.Removed {
		n += im.Size
	}
	return n
}

// PruneOld removes box images nothing needs. An image goes only when ALL hold:
//   - it is a box image: it carries LabelHash, and every repository it is named
//     under is a box repository (BoxRepos plus in.Repos); an image with no name at
//     all (a moved tag left it dangling) counts by its label;
//   - it is not the image just set up, nor any image its names resolve to;
//   - no container, running or stopped, uses it (container ls -a);
//   - no name in defaultPins or in.Pins resolves to it;
//   - it is not a rollback image (in.RollbackPins) that cannot be pulled again.
//
// Pins are resolved by asking the daemon, so a tag moved since or a digest spelled
// another way cannot hide a pinned image. Anything the daemon will not answer
// (a listing, a pin it cannot read) is an error returned before the first
// removal: an unknown is never read as "unused". Removal never forces: docker
// itself refuses an image a container started to use since the listing, and that
// is reported as a failure. One failed removal does not stop the others.
func (d Docker) PruneOld(ctx context.Context, in PruneInput) (PruneResult, error) {
	var res PruneResult
	keepID, ok, err := d.ImageID(ctx, in.Keep)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, fmt.Errorf("the image just set up, %s, is not in the store", in.Keep)
	}
	pinned, err := d.resolveAll(ctx, append(append([]string(nil), defaultPins...), in.Pins...))
	if err != nil {
		return res, err
	}
	rollback, err := d.resolveAll(ctx, in.RollbackPins)
	if err != nil {
		return res, err
	}
	imgs, err := d.listImages(ctx)
	if err != nil {
		return res, err
	}
	used, err := d.usedImageIDs(ctx)
	if err != nil {
		return res, err
	}
	repos := map[string]bool{}
	for _, r := range append(append([]string(nil), BoxRepos...), in.Repos...) {
		repos[normRepo(r)] = true
	}
	var due []StoreImage
	for _, im := range imgs {
		switch {
		case !im.isBox(repos), im.ID == keepID, used[im.ID], pinned[im.ID]:
		case rollback[im.ID] && !im.HasDigest:
			res.KeptForRollback = append(res.KeptForRollback, im)
		default:
			due = append(due, im)
		}
	}
	if err := d.fillSizes(ctx, due); err != nil {
		return res, err
	}
	if in.beforeRemove != nil {
		in.beforeRemove()
	}
	for _, im := range due {
		if err := d.removeImage(ctx, im); err != nil {
			res.Failed = append(res.Failed, PruneFailure{Image: im, Err: err})
			continue
		}
		res.Removed = append(res.Removed, im)
	}
	return res, nil
}

// isBox: built from CuliBox's Dockerfile and named only under box repositories.
func (i StoreImage) isBox(repos map[string]bool) bool {
	if !i.Labeled {
		return false
	}
	for _, r := range i.Repos {
		if !repos[normRepo(r)] {
			return false
		}
	}
	return true
}

// normRepo drops the Docker Hub registry prefix docker leaves out when it lists.
func normRepo(r string) string {
	r = strings.TrimPrefix(r, "index.docker.io/")
	return strings.TrimPrefix(r, "docker.io/")
}

// RepoOf is a ref's repository: no tag, no digest. "" for an empty ref.
func RepoOf(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	r, _ := splitTag(ref)
	return r
}

// inspectOut runs `image inspect --format format ref…` and returns its stdout.
// missing is true when the daemon says there is no such image (single ref).
func (d Docker) inspectOut(ctx context.Context, format string, refs ...string) (out string, missing bool, err error) {
	b, err := d.command(ctx, append([]string{"image", "inspect", "--format", format}, refs...)...).Output()
	if err == nil {
		return string(b), false, nil
	}
	var stderr string
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr = strings.TrimSpace(string(ee.Stderr))
		if strings.Contains(strings.ToLower(stderr), "no such image") {
			return "", true, nil
		}
	}
	return "", false, fmt.Errorf("docker image inspect: %w: %s", err, stderr)
}

// ImageID is the ID the daemon holds under a name (or an ID); ok is false when
// it holds none. Any other failure is an error.
func (d Docker) ImageID(ctx context.Context, ref string) (id string, ok bool, err error) {
	out, missing, err := d.inspectOut(ctx, "{{.ID}}", ref)
	if err != nil || missing {
		return "", false, err
	}
	id = strings.TrimSpace(out)
	return id, id != "", nil
}

func (d Docker) resolveAll(ctx context.Context, refs []string) (map[string]bool, error) {
	ids := map[string]bool{}
	for _, r := range refs {
		id, ok, err := d.ImageID(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("cannot read pinned image %q: %w", r, err)
		}
		if ok {
			ids[id] = true
		}
	}
	return ids, nil
}

// listImages is every image in the store with its names and whether it is a box
// image by label. One row per name, as docker prints them; rows of one ID merge.
func (d Docker) listImages(ctx context.Context) ([]StoreImage, error) {
	out, err := d.command(ctx, "image", "ls", "--no-trunc", "--digests", "--format",
		"{{.ID}}\t{{.Repository}}\t{{.Tag}}\t{{.Digest}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker image ls: %w", err)
	}
	lab, err := d.command(ctx, "image", "ls", "--no-trunc", "-q", "--filter", "label="+LabelHash).Output()
	if err != nil {
		return nil, fmt.Errorf("docker image ls: %w", err)
	}
	labeled := map[string]bool{}
	for _, l := range strings.Fields(string(lab)) {
		labeled[l] = true
	}
	var imgs []StoreImage
	at := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 || !strings.HasPrefix(f[0], "sha256:") {
			return nil, fmt.Errorf("docker image ls: unexpected row %q", line)
		}
		id, repo, tag, digest := f[0], f[1], f[2], f[3]
		i, seen := at[id]
		if !seen {
			i = len(imgs)
			at[id] = i
			imgs = append(imgs, StoreImage{ID: id, Labeled: labeled[id]})
		}
		im := &imgs[i]
		if repo != "<none>" {
			if !containsStr(im.Repos, repo) {
				im.Repos = append(im.Repos, repo)
			}
			switch {
			case tag != "<none>":
				im.Refs = append(im.Refs, repo+":"+tag)
			case digest != "<none>":
				im.Refs = append(im.Refs, repo+"@"+digest)
			}
		}
		if digest != "<none>" {
			im.HasDigest = true
		}
	}
	return imgs, nil
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// usedImageIDs is the image ID of every container in any state (created,
// running, paused, exited, dead). Its own docker calls, so that a container
// created from a name that has since moved still counts by the ID it started on.
func (d Docker) usedImageIDs(ctx context.Context) (map[string]bool, error) {
	out, err := d.command(ctx, "container", "ls", "-a", "-q", "--no-trunc").Output()
	if err != nil {
		return nil, fmt.Errorf("docker container ls: %w", err)
	}
	used := map[string]bool{}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return used, nil
	}
	out, err = d.command(ctx, append([]string{"container", "inspect", "--format", "{{.Image}}"}, ids...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker container inspect: %w", err)
	}
	for _, l := range strings.Fields(string(out)) {
		used[l] = true
	}
	return used, nil
}

// fillSizes reads each due image's size in bytes (docker image inspect).
func (d Docker) fillSizes(ctx context.Context, due []StoreImage) error {
	if len(due) == 0 {
		return nil
	}
	ids := make([]string, len(due))
	for i, im := range due {
		ids[i] = im.ID
	}
	out, _, err := d.inspectOut(ctx, "{{.ID}}\t{{.Size}}", ids...)
	if err != nil {
		return err
	}
	sizes := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, sz, ok := strings.Cut(line, "\t")
		n, perr := strconv.ParseInt(strings.TrimSpace(sz), 10, 64)
		if !ok || perr != nil {
			return fmt.Errorf("docker image inspect: unexpected row %q", line)
		}
		sizes[id] = n
	}
	for i := range due {
		due[i].Size = sizes[due[i].ID]
	}
	return nil
}

// removeImage removes every name of the image in one `image rm`, without force:
// the last name takes the image with it, and docker refuses an image a container
// uses. An image with no name goes by its ID. It then checks the image is gone,
// so a name that survives (a reference this listing did not show) is reported
// and its bytes are not counted as freed.
func (d Docker) removeImage(ctx context.Context, im StoreImage) error {
	targets := im.Refs
	if len(targets) == 0 {
		targets = []string{im.ID}
	}
	out, err := d.command(ctx, append([]string{"image", "rm"}, targets...)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	if _, still, err := d.ImageID(ctx, im.ID); err == nil && still {
		return errors.New("untagged, but the image is still in the store (another reference holds it)")
	}
	return nil
}
