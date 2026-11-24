// Package boximage builds the box image from the Dockerfile embedded in the
// binary. The macOS binary is downloaded without the source tree, so the image
// recipe has to travel inside it.
package boximage

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed Dockerfile
var dockerfile []byte

// zshrc is the box's zsh defaults, COPY'd by the Dockerfile, which pins its
// sha256: editing it changes the Dockerfile, and so Hash().
//
//go:embed zshrc
var zshrc []byte

// LabelHash records which Dockerfile built an image, so doctor can tell an
// image older than this binary.
const LabelHash = "io.culiops.agent-box.dockerfile"

// LabelClaudeCode holds the Claude Code version the image was built with, so the
// version can be read from the image without running it.
const LabelClaudeCode = "io.culiops.agent-box.claude-code"

var claudeCodeVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+.][0-9A-Za-z.-]{1,32})?$`)

// ValidClaudeCode reports whether s has the shape of a Claude Code version: x.y.z
// with an optional short suffix. A label value is checked with it before it is
// taken for a version.
func ValidClaudeCode(s string) bool {
	return claudeCodeVersion.MatchString(s)
}

// Hash is the first 12 hex digits of the embedded Dockerfile's sha256.
func Hash() string {
	sum := sha256.Sum256(dockerfile)
	return hex.EncodeToString(sum[:])[:12]
}

// PublishedRepo is the registry repository CI publishes the box image to.
const PublishedRepo = "ghcr.io/culiops/culibox"

// published is the multi-arch index digest of the published image built from
// this binary's Dockerfile. Only a release build sets it (scripts/release/build.sh,
// -ldflags -X); a build from source leaves it empty and builds the image locally.
var published string

// publishedRef is the only form a baked ref may take: the published repository,
// pinned by digest.
var publishedRef = regexp.MustCompile(`^ghcr\.io/culiops/culibox@sha256:[0-9a-f]{64}$`)

// ValidPublished reports whether ref is a digest of the published repository.
func ValidPublished(ref string) bool {
	return publishedRef.MatchString(ref)
}

// Published is the ref `box setup` pulls for the default image, or "" when this
// binary carries none (a source build) or a malformed one, which never reaches
// docker.
func Published() string {
	if !ValidPublished(published) {
		return ""
	}
	return published
}

// Docker is the docker CLI with agent-box's global flags (see box.CLIClient).
type Docker struct {
	Bin  string
	Args []string
	// ExtraCA is the canonical PEM of the operator's extra CA certificates
	// (nil when off). Build puts it in the context for the build steps to
	// trust; it never reaches the shipped image.
	ExtraCA []byte
}

func (d Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, d.Bin, append(append([]string(nil), d.Args...), args...)...)
}

// ServerArch is the daemon's architecture in docker platform terms (arm64, amd64).
func (d Docker) ServerArch(ctx context.Context) (string, error) {
	out, err := d.command(ctx, "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Tag gives the local image src a second name, dst (`docker tag src dst`). It
// copies no layers and removes nothing: both names point at one image ID.
func (d Docker) Tag(ctx context.Context, src, dst string) error {
	out, err := d.command(ctx, "tag", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker tag %s %s: %w: %s", src, dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AdoptLegacyDefault gives an image held under the pre-rename default name the
// new default name: when image is missing and legacy is present, it tags legacy
// as image. The legacy tag stays. It reports whether it tagged. An image that
// is already there, or neither, changes nothing; the error is the daemon's, and
// means "no answer", so the caller may ask again.
func AdoptLegacyDefault(ctx context.Context, d interface {
	Inspect(context.Context, string) (ImageState, error)
	Tag(context.Context, string, string) error
}, image, legacy string) (adopted bool, err error) {
	st, err := d.Inspect(ctx, image)
	if err != nil || st.Present {
		return false, err
	}
	old, err := d.Inspect(ctx, legacy)
	if err != nil || !old.Present {
		return false, err
	}
	if err := d.Tag(ctx, legacy, image); err != nil {
		return false, err
	}
	return true, nil
}

// extraCAName is the context file the Dockerfile COPYs. It is always present,
// empty when no CA is configured, so a COPY never fails and the argv is the
// same with CAs on or off.
const extraCAName = "extra-ca.pem"

// contextTar is the build context: the Dockerfile, extra-ca.pem and zshrc, streamed
// on stdin, so nothing is written to disk.
func contextTar(extraCA []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct {
		name string
		body []byte
	}{{"Dockerfile", dockerfile}, {extraCAName, extraCA}, {"zshrc", zshrc}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tailLen is how much of a failed build's output OutputError keeps.
const tailLen = 64 << 10

// OutputError is a failed docker command with the end of its output, where the
// cause is. Callers match it with errors.As to look for a known cause.
type OutputError struct {
	Err  error
	Tail string
}

func (e *OutputError) Error() string { return e.Err.Error() }
func (e *OutputError) Unwrap() error { return e.Err }

// tailWriter keeps the last tailLen bytes written to it. It compacts at twice
// that, so memory is bounded and the copying is amortized.
type tailWriter struct{ buf []byte }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*tailLen {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-tailLen:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	b := t.buf
	if len(b) > tailLen {
		b = b[len(b)-tailLen:]
	}
	return string(b)
}

// Build builds the image for the daemon's own architecture and gives it every
// name in tags. --platform and --pull are not optional: a local node:* tag of
// another arch once produced an emulated box about 8x slower, with no error
// anywhere.
func (d Docker) Build(ctx context.Context, tags []string, w io.Writer) error {
	if len(tags) == 0 {
		return errors.New("docker build: no image tag given")
	}
	arch, err := d.ServerArch(ctx)
	if err != nil {
		return err
	}
	tarball, err := contextTar(d.ExtraCA)
	if err != nil {
		return err
	}
	args := []string{"build", "--pull", "--platform", "linux/" + arch, "--label", LabelHash + "=" + Hash()}
	for _, t := range tags {
		args = append(args, "-t", t)
	}
	cmd := d.command(ctx, append(args, "-")...)
	cmd.Stdin = bytes.NewReader(tarball)
	// One writer for both streams (see Pull): output still streams to w.
	var tail tailWriter
	cmd.Stdout = io.MultiWriter(w, &tail)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		return &OutputError{Err: fmt.Errorf("docker build %s: %w", strings.Join(tags, ", "), err), Tail: tail.String()}
	}
	return nil
}

// Pull fetches a remote image for the given architecture. --platform is not
// optional here either: a multi-arch index digest otherwise resolves to
// whatever docker picks, and an emulated box is slow with no error anywhere.
// docker's output goes to w; it is also kept so a failure carries its cause.
// Both streams share ONE writer: os/exec copies two distinct writers from two
// goroutines, and w (a bytes.Buffer in tests) is not safe for concurrent use.
func (d Docker) Pull(ctx context.Context, image, arch string, w io.Writer) error {
	var out bytes.Buffer
	cmd := d.command(ctx, "pull", "--platform", "linux/"+arch, image)
	cmd.Stdout = io.MultiWriter(w, &out)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker pull %s: %w: %s", image, err, strings.TrimSpace(out.String()))
	}
	return nil
}

// datedTag is what a build date looks like as a tag: YYYY-MM-DD, with .2 to .9
// when that day's tag is taken.
var datedTag = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}(\.[2-9])?$`)

// splitTag cuts a ref at its tag: the part after the last ":" that follows the
// last "/" (a ":" before that is a registry port).
func splitTag(image string) (repo, tag string) {
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, ""
}

// IsDated reports whether image is pinned: a tag in the dated form that Build
// callers mint. A digest ref is not a dated tag.
func IsDated(image string) bool {
	if strings.Contains(image, "@") {
		return false
	}
	_, tag := splitTag(image)
	return datedTag.MatchString(tag)
}

// IsRemote reports whether image names a registry, so it is pulled and never
// built: any digest ref, or a ref whose first path component is a registry
// host (it holds a "." or ":", or is localhost: docker's own rule).
func IsRemote(image string) bool {
	if strings.Contains(image, "@") {
		return true
	}
	host, _, ok := strings.Cut(image, "/")
	if !ok {
		return false
	}
	return strings.ContainsAny(host, ".:") || host == "localhost"
}

// Repo is the ref without its tag. A digest ref has no tag to drop, so it errors.
func Repo(image string) (string, error) {
	if strings.Contains(image, "@") {
		return "", fmt.Errorf("%s is a digest, not a repo:tag", image)
	}
	repo, _ := splitTag(image)
	return repo, nil
}

// NextDatedTag returns the first tag of repo that the daemon does not hold:
// <repo>:<UTC date>, then .2 to .9. A dated tag is immutable, so a taken one
// is never reused. taken names tags to skip even when the daemon lacks them: a
// configured pin counts as taken, so a rebuild never builds the pin itself.
func (d Docker) NextDatedTag(ctx context.Context, repo string, now time.Time, taken ...string) (string, error) {
	day := now.UTC().Format("2006-01-02")
	for i := 1; i <= 9; i++ {
		tag := repo + ":" + day
		if i > 1 {
			tag += fmt.Sprintf(".%d", i)
		}
		if slices.Contains(taken, tag) {
			continue
		}
		st, err := d.Inspect(ctx, tag)
		if err != nil {
			return "", err
		}
		if !st.Present {
			return tag, nil
		}
	}
	return "", fmt.Errorf("every dated tag of %s for %s is taken", repo, day)
}

// ImageState is what the daemon holds under the image name.
type ImageState struct {
	Present    bool
	Arch, Hash string
}

// Inspect reads the image's architecture and Dockerfile label. Stdout only: a
// CLI or context warning on stderr must not become part of the arch, or a
// current image reads as built for another architecture. stderr is read only
// on failure, to tell a missing image from a real error.
func (d Docker) Inspect(ctx context.Context, image string) (ImageState, error) {
	out, err := d.command(ctx, "image", "inspect", "--format",
		`{{.Architecture}}|{{index .Config.Labels "`+LabelHash+`"}}`, image).Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
			if strings.Contains(strings.ToLower(stderr), "no such image") {
				return ImageState{}, nil
			}
		}
		return ImageState{}, fmt.Errorf("docker image inspect: %w: %s", err, stderr)
	}
	s := strings.TrimSpace(string(out))
	arch, hash, _ := strings.Cut(s, "|")
	if hash == "<no value>" {
		hash = ""
	}
	return ImageState{Present: true, Arch: arch, Hash: hash}, nil
}

// Check grades the image against the daemon. A missing image or one built for
// another architecture cannot run a box (fatal). An image from an older
// Dockerfile still works, so it only warns. msg is "" when all is well.
func (s ImageState) Check(serverArch string) (fatal bool, msg string) {
	switch {
	case !s.Present:
		return true, "box image not set up; run `culibox box setup`"
	case s.Arch != serverArch:
		return true, fmt.Sprintf("box image is %s but the daemon is %s (it would run emulated); run `culibox box setup`", s.Arch, serverArch)
	case s.Hash != Hash():
		return false, "box image is older than this CuliBox; run `culibox box setup`"
	}
	return false, ""
}

// CheckFor is Check for the configured image. A remote image is pulled by box
// setup and never rebuilt, so its advice is to pull. A dated tag is pinned: agent-box
// never builds, rebuilds or pulls it, so a missing one is fatal with advice to
// pin a present tag, and a stale one is fine to keep.
func (s ImageState) CheckFor(image, serverArch string) (fatal bool, msg string) {
	if IsRemote(image) {
		switch {
		case !s.Present:
			return true, fmt.Sprintf("box image %s is not on this daemon; run `culibox box setup` to pull it", image)
		case s.Arch != serverArch:
			return true, fmt.Sprintf("box image %s is linux/%s but the daemon is %s; pin the multi-arch index digest and run `culibox box setup`", image, s.Arch, serverArch)
		case s.Hash != Hash():
			return false, "this image was built from an older Dockerfile; pull a newer digest from ghcr"
		}
		return false, ""
	}
	if !IsDated(image) {
		return s.Check(serverArch)
	}
	switch {
	case !s.Present:
		return true, fmt.Sprintf("pinned box image %s is missing; CuliBox never rebuilds or pulls a pinned tag: set box_image to a present dated tag (docker images %s), or run culibox box setup -rebuild and pin the tag it prints", image, mustRepo(image))
	case s.Arch != serverArch:
		// box setup never rebuilds a pin; -rebuild builds a new dated tag to pin.
		return true, fmt.Sprintf("pinned box image %s is %s but the daemon is %s (it would run emulated); run `culibox box setup -rebuild` and pin the dated tag it prints", image, s.Arch, serverArch)
	case s.Hash == Hash():
		return false, ""
	}
	return false, fmt.Sprintf("pinned to %s, built from an older Dockerfile than this CuliBox (fine to keep); culibox box setup -rebuild builds a new dated tag you can pin", image)
}

// mustRepo drops Repo's error: it is only called for a dated image, which
// IsDated has already shown is not a digest ref.
func mustRepo(image string) string {
	r, _ := Repo(image)
	return r
}
