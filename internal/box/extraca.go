package box

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/extraca"
)

const (
	caDirName = "ca"
	// maxBaseBundle bounds the image's own bundle (~200 KiB today). Anything far
	// beyond it is not a CA bundle, and is not copied into a mounted file.
	maxBaseBundle = 4 << 20
)

// caBundle returns the host path of the trust bundle every box of this image
// mounts: the image's own /etc/ssl/certs/ca-certificates.crt followed by the
// configured extra CAs, at <StateRoot>/ca/bundle-<imageID12>-<caSetHash12>.pem.
//
// The cache key is the image ID, so a rebuilt image gets a bundle made from its
// own trust store and one from another image is never mounted. The ID is asked
// of docker on every call (one cheap round trip, only when a box is created);
// the file is extracted from the image only when that ID has no bundle yet.
//
// The extraction never runs anything from the image: docker create, docker cp,
// docker rm. All of it goes through Client.Run, so on macOS it reaches the same
// VM daemon as the box does. The file is written under an os.Root and renamed
// into place, so a half-written bundle is never mounted. Mode 0644: the CAs are
// public, and the box user must read it through the VM's shared mount.
//
// Caller holds opMu.
func (m *Manager) caBundle(ctx context.Context) (string, error) {
	out, err := m.opts.Client.Run(ctx, []string{"image", "inspect", "-f", "{{.Id}}", m.opts.Image})
	if err != nil {
		return "", fmt.Errorf("inspecting image %s: %w", m.opts.Image, err)
	}
	imageID, err := parseImageID(out)
	if err != nil {
		return "", err
	}
	rel := path.Join(caDirName, "bundle-"+strings.TrimPrefix(imageID, "sha256:")[:12]+"-"+m.opts.ExtraCA.Hash()+".pem")
	host := filepath.Join(m.opts.StateRoot, filepath.FromSlash(rel))
	extra := m.opts.ExtraCA.PEM()

	root, err := os.OpenRoot(m.opts.StateRoot)
	if err != nil {
		return "", fmt.Errorf("opening state root: %w", err)
	}
	defer root.Close()
	if fi, err := root.Stat(rel); err == nil && fi.Mode().IsRegular() && fi.Size() > int64(len(extra)) {
		return host, nil
	}

	if err := root.MkdirAll(caDirName, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Join(m.opts.StateRoot, caDirName), err)
	}
	base, err := m.extractBaseBundle(ctx, root, imageID)
	if err != nil {
		return "", err
	}
	// One newline between the image's last block and ours, whatever it ended in.
	body := append(bytes.TrimRight(base, "\n"), '\n')
	body = append(body, extra...)

	tmp, err := tempName(".bundle-")
	if err != nil {
		return "", err
	}
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("writing the trust bundle: %w", err)
	}
	werr := errors.Join(writeAll(f, body), f.Chmod(0o644), f.Sync())
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = root.Remove(tmp)
		return "", fmt.Errorf("writing the trust bundle: %w", err)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return "", fmt.Errorf("installing the trust bundle: %w", err)
	}
	return host, nil
}

func writeAll(f *os.File, b []byte) error {
	_, err := f.Write(b)
	return err
}

// lastLine is the last non-empty line of docker output. Client.Run returns
// stdout and stderr together, and a warning (IPv4 forwarding disabled, a
// platform mismatch) comes before the answer.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// parseImageID reads `docker image inspect -f {{.Id}}` output: "sha256:" and 64
// lowercase hex, which is returned whole. The ID becomes part of a path and of
// an argument, so anything else is refused.
func parseImageID(out string) (string, error) {
	id := lastLine(out)
	if hexPart, ok := strings.CutPrefix(id, "sha256:"); !ok || !isLowerHex64(hexPart) {
		return "", fmt.Errorf("unexpected image id %q", out)
	}
	return id, nil
}

// parseContainerID reads `docker create` output: 64 lowercase hex.
func parseContainerID(out string) (string, error) {
	cid := lastLine(out)
	if !isLowerHex64(cid) {
		return "", fmt.Errorf("unexpected container id %q from docker create", out)
	}
	return cid, nil
}

// tempName is a fresh, unguessable name inside ca/ (root-relative).
func tempName(prefix string) (string, error) {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	return path.Join(caDirName, prefix+hex.EncodeToString(rnd[:])+".tmp"), nil
}

// extractBaseBundle copies the image's trust bundle out of a container that is
// created and removed but never started. docker cp writes to a fresh name inside
// ca/ (never outside the data root), which is read back through root and removed.
//
// The container is made from imageID, the ID caBundle just inspected, not from
// the tag, so the bundle's name and its content always come from the same image.
// It carries LabelCAScratch (so a crash leaves something Reconcile and Sweep can
// reap) and an explicit `true` command; it is never started.
func (m *Manager) extractBaseBundle(ctx context.Context, root *os.Root, imageID string) ([]byte, error) {
	out, err := m.opts.Client.Run(ctx, []string{"create", "--pull=never",
		"--label", LabelCAScratch + "=" + m.opts.Instance, imageID, "true"})
	if err != nil {
		return nil, fmt.Errorf("creating a container to read the image's CA bundle: %w", err)
	}
	cid, err := parseContainerID(out)
	if err != nil {
		// Not removed: nothing here is an id docker could have handed out.
		return nil, err
	}
	defer func() {
		// Not on the caller's ctx: a cancelled spawn must still clean up.
		_, _ = m.opts.Client.Run(context.WithoutCancel(ctx), []string{"rm", "-f", cid})
	}()

	rel, err := tempName(".base-")
	if err != nil {
		return nil, err
	}
	defer root.Remove(rel)
	dst := filepath.Join(m.opts.StateRoot, filepath.FromSlash(rel))
	if _, err := m.opts.Client.Run(ctx, []string{"cp", cid + ":" + extraca.ContainerBundle, dst}); err != nil {
		return nil, fmt.Errorf("copying the image's CA bundle: %w", err)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxBaseBundle {
		return nil, fmt.Errorf("the image's %s is not a regular file of a sane size", extraca.ContainerBundle)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBaseBundle+1))
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(b, []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, fmt.Errorf("the image's %s holds no certificates", extraca.ContainerBundle)
	}
	return b, nil
}
