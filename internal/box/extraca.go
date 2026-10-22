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
	id, err := imageID12(out)
	if err != nil {
		return "", err
	}
	rel := path.Join(caDirName, "bundle-"+id+"-"+m.opts.ExtraCA.Hash()+".pem")
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
	base, err := m.extractBaseBundle(ctx, root)
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

// imageID12 turns `docker image inspect -f {{.Id}}` output into the 12 hex
// characters that name the bundle file. The ID becomes part of a path, so
// anything but hex is refused.
func imageID12(out string) (string, error) {
	id := strings.TrimPrefix(strings.TrimSpace(out), "sha256:")
	if len(id) < 12 {
		return "", fmt.Errorf("unexpected image id %q", out)
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("unexpected image id %q", out)
		}
	}
	return id[:12], nil
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
func (m *Manager) extractBaseBundle(ctx context.Context, root *os.Root) ([]byte, error) {
	cid, err := m.opts.Client.Run(ctx, []string{"create", "--pull=never", m.opts.Image})
	if err != nil {
		return nil, fmt.Errorf("creating a container to read the image's CA bundle: %w", err)
	}
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return nil, errors.New("docker create returned no container id")
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
