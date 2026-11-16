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
// not here: it is never pulled or run. Nothing else is a box image, whatever
// label it carries: an image built FROM the box image inherits the label.
var BoxRepos = []string{
	"culiops/culibox", "culiops/agent-box",
	"ghcr.io/culiops/culibox", "ghcr.io/culiops/agent-box",
}

// registryRepos are the BoxRepos CuliBox pulls (IsRemote): an image named only
// under them, with a registry digest, can be pulled again by `box setup`. The Docker
// Hub forms are only ever built here, never pulled, so a digest shown on one (the
// containerd image store shows one on every image, local builds too) says nothing.
var registryRepos = []string{"ghcr.io/culiops/culibox", "ghcr.io/culiops/agent-box"}

// defaultPins are the names an instance runs when its config names no box_image,
// or no config exists at all: the default, and the pre-rename default that a
// not-yet-upgraded CuliBox (a rollback target) still runs. An image holding
// either name is never pruned, whatever configs are visible.
var defaultPins = []string{"culiops/culibox:base", "culiops/agent-box:base"}

// Ref is one name of an image: a row of `docker image ls --digests`.
type Ref struct{ Repo, Tag, Digest string }

// StoreImage is one image in the daemon's store, with every name it has.
type StoreImage struct {
	ID      string   // the full ID, sha256:…
	Refs    []string // repo:tag and repo@digest names; none = dangling
	Names   []Ref    // the rows behind Refs (repository known)
	Labeled bool     // carries LabelHash: built from CuliBox's Dockerfile
	Size    int64    // bytes, from image inspect (filled for removal candidates)
}

// Name is how output names the image: its names, or a short ID when it has none.
func (i StoreImage) Name() string {
	if len(i.Refs) > 0 {
		return strings.Join(i.Refs, ", ")
	}
	short := strings.TrimPrefix(i.ID, "sha256:")
	return short[:min(12, len(short))]
}

// RmArgs is what to give `docker image rm` for this image: its names when it has
// several, else its ID (docker refuses a bare ID that has several names).
func (i StoreImage) RmArgs() []string {
	if len(i.Refs) > 1 {
		return i.Refs
	}
	return []string{i.ID}
}

// isBox: built from CuliBox's Dockerfile and named only under BoxRepos. An image
// with no name at all counts only when it is the one this setup orphaned: a
// dangling labelled image may be anyone's derived image.
func (i StoreImage) isBox(orphan string) bool {
	if !i.Labeled {
		return false
	}
	if len(i.Names) == 0 {
		return orphan != "" && i.ID == orphan
	}
	for _, n := range i.Names {
		if !inRepos(BoxRepos, n.Repo) {
			return false
		}
	}
	return true
}

// rePullable: named only under the registry repositories, with a registry digest.
func (i StoreImage) rePullable() bool {
	digest := false
	for _, n := range i.Names {
		if !inRepos(registryRepos, n.Repo) {
			return false
		}
		digest = digest || n.Digest != ""
	}
	return digest
}

func inRepos(set []string, repo string) bool {
	repo = normRepo(repo)
	for _, r := range set {
		if r == repo {
			return true
		}
	}
	return false
}

// PruneInput is what PruneOld keeps, and what it may remove.
type PruneInput struct {
	// Keep are the names of the image(s) the setup just made current: the
	// configured image, and the dated tag it built. All are kept, by ID.
	Keep []string
	// Orphan is the image ID the configured name held before this setup moved or
	// replaced it ("" for none): the image this setup orphaned.
	Orphan string
	// Pins are the images a visible instance config names: never removed.
	Pins []string
	// RollbackPins are the images a retained upgrade backup's config names, which a
	// documented rollback restores, and that cannot be pulled again: never removed.
	RollbackPins []string
	// Local also removes the unused box images that cannot be pulled again (the
	// default only lists them).
	Local bool

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
	// KeptLocal are unused box images that cannot be pulled again, left alone
	// because Local was not set. Sizes are filled.
	KeptLocal []StoreImage
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

// OrphanOf is the ID the name holds now, for the caller to read BEFORE a setup
// moves the name and hand to PruneOld as Orphan afterwards. "" when the name is
// not in a box repository (a user's own box_image repository is never swept), is
// not held, or the daemon does not say.
func (d Docker) OrphanOf(ctx context.Context, name string) string {
	if !inRepos(BoxRepos, RepoOf(name)) {
		return ""
	}
	id, ok, err := d.ImageID(ctx, name)
	if err != nil || !ok {
		return ""
	}
	return id
}

// PruneOld removes box images nothing needs. An image is a CANDIDATE only when ALL hold:
//   - it is a box image: it carries LabelHash and every repository it is named
//     under is in BoxRepos (nothing a visible config names widens that);
//   - it is not an image just set up (Keep), by ID, under any of its names;
//   - no container, running or stopped, uses it (container ls -a);
//   - no name in defaultPins or in.Pins resolves to it;
//   - it is not an image a retained backup would restore (RollbackPins).
//
// A candidate is removed when it is the image this setup orphaned (in.Orphan: the
// default :base upgrade leak) or can be pulled again (rePullable: only ghcr
// names, with a registry digest). Every other candidate is a local build: nothing
// can bring it back, and an instance this process cannot see may pin it (boxes are
// removed when idle, so "no container" is the normal state of another instance),
// so it is only returned in KeptLocal, and removed when in.Local is set. A
// dangling image is never a candidate except the orphan.
//
// The daemon is listed first and pins are resolved afterwards, by asking it, so a
// tag cannot hide a pinned image. Anything the daemon will not answer is an error
// returned before the first removal: an unknown is never read as "unused".
// Removal never forces; the containers are read again just before it, and docker
// itself refuses what a container still uses. One failure does not stop the others.
func (d Docker) PruneOld(ctx context.Context, in PruneInput) (PruneResult, error) {
	var res PruneResult
	if len(in.Keep) == 0 {
		return res, errors.New("no image just set up to keep")
	}
	imgs, err := d.listImages(ctx)
	if err != nil {
		return res, err
	}
	used, err := d.usedImageIDs(ctx)
	if err != nil {
		return res, err
	}
	keep := map[string]bool{}
	for _, k := range in.Keep {
		id, ok, err := d.ImageID(ctx, k)
		if err != nil {
			return res, err
		}
		if !ok {
			return res, fmt.Errorf("the image just set up, %s, is not in the store", k)
		}
		keep[id] = true
	}
	pinned, err := d.resolveAll(ctx, append(append([]string(nil), defaultPins...), in.Pins...))
	if err != nil {
		return res, err
	}
	rollback, err := d.resolveAll(ctx, in.RollbackPins)
	if err != nil {
		return res, err
	}
	var due, local []StoreImage
	for _, im := range imgs {
		switch {
		case !im.isBox(in.Orphan), keep[im.ID], used[im.ID], pinned[im.ID]:
		case rollback[im.ID]:
			res.KeptForRollback = append(res.KeptForRollback, im)
		case im.ID == in.Orphan, im.rePullable(), in.Local:
			due = append(due, im)
		default:
			local = append(local, im)
		}
	}
	if err := d.fillSizes(ctx, due); err != nil {
		return res, err
	}
	if err := d.fillSizes(ctx, local); err != nil {
		return res, err
	}
	res.KeptLocal = local
	if in.beforeRemove != nil {
		in.beforeRemove()
	}
	used, err = d.usedImageIDs(ctx)
	if err != nil {
		return res, err
	}
	for _, im := range due {
		if used[im.ID] {
			res.Failed = append(res.Failed, PruneFailure{Image: im, Err: errors.New("a container started using it since the listing; kept")})
			continue
		}
		if err := d.removeImage(ctx, im); err != nil {
			res.Failed = append(res.Failed, PruneFailure{Image: im, Err: err})
			continue
		}
		res.Removed = append(res.Removed, im)
	}
	return res, nil
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
		if repo == "<none>" {
			continue
		}
		n := Ref{Repo: repo}
		if tag != "<none>" {
			n.Tag = tag
		}
		if digest != "<none>" {
			n.Digest = digest
		}
		im.Names = append(im.Names, n)
		switch {
		case n.Tag != "":
			im.Refs = append(im.Refs, repo+":"+n.Tag)
		case n.Digest != "":
			im.Refs = append(im.Refs, repo+"@"+n.Digest)
		}
	}
	return imgs, nil
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

// fillSizes reads each image's size in bytes (docker image inspect).
func (d Docker) fillSizes(ctx context.Context, imgs []StoreImage) error {
	if len(imgs) == 0 {
		return nil
	}
	ids := make([]string, len(imgs))
	for i, im := range imgs {
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
	for i := range imgs {
		imgs[i].Size = sizes[imgs[i].ID]
	}
	return nil
}

// removeImage removes the image without force: by its ID when it has at most one
// name (so no half-done untag is possible), else by each of its names, the last
// of which takes the image with it (docker refuses a bare ID with several names,
// and -f would also remove an image a stopped container uses). It then checks the
// image is gone: an image still there, or one whose state cannot be read, is a
// failure and its bytes are not counted as freed.
func (d Docker) removeImage(ctx context.Context, im StoreImage) error {
	out, err := d.command(ctx, append([]string{"image", "rm"}, im.RmArgs()...)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	_, still, err := d.ImageID(ctx, im.ID)
	switch {
	case err != nil:
		return fmt.Errorf("removed, but could not confirm it is gone: %w", err)
	case still:
		return errors.New("untagged, but the image is still in the store (another reference holds it)")
	}
	return nil
}
