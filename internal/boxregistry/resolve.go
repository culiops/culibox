package boxregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
)

// Strings the registry supplies are printed with %q: this runs in a GitHub
// Actions step, which reads "::" commands from a line's start.

// maxBody caps every registry response: an index, a manifest or an image config
// is a few KB.
const maxBody = 4 << 20

const (
	ociIndex      = "application/vnd.oci.image.index.v1+json"
	dockerList    = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociManifest   = "application/vnd.oci.image.manifest.v1+json"
	dockerSchema2 = "application/vnd.docker.distribution.manifest.v2+json"
)

// ErrNotFound marks a Resolve that failed because the registry has no index under
// dockerfile-<hash>: the image for that Dockerfile is not published (yet). Only
// that: a 404 on anything else the registry names is a different failure.
var ErrNotFound = errors.New("no image is published for this Dockerfile")

// tagMissing is the Resolve error for an absent tag. Its text is the wrapped
// error's, unchanged (release.yml logs it), and it is ErrNotFound.
type tagMissing struct{ err error }

func (e tagMissing) Error() string        { return e.err.Error() }
func (e tagMissing) Unwrap() error        { return e.err }
func (e tagMissing) Is(target error) bool { return target == ErrNotFound }

// statusError is a registry answer other than 200. Its text is what fetch always said.
type statusError struct {
	path, status string
	code         int
}

func (e *statusError) Error() string { return fmt.Sprintf("GET %s: %s", e.path, e.status) }

// digestForm is the only digest form fetched: it becomes part of a request path
// and of error messages, so nothing else the registry names may get that far.
var digestForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// wantPlatforms are the arches box-image.yml publishes, and all an index may hold.
var wantPlatforms = []string{"linux/amd64", "linux/arm64"}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

// maxLayerSize is the most a layer may claim: sizes are the registry's word and
// feed a sum shown to the user.
const maxLayerSize = 1 << 40

// Layer is one compressed layer of a platform's image, as its manifest lists it.
type Layer struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Platform is what one platform's manifest and image config say about the image.
type Platform struct {
	Created    time.Time `json:"created,omitzero"`      // zero when absent or not RFC 3339
	ClaudeCode string    `json:"claude_code,omitempty"` // boximage.ValidClaudeCode, "" when absent or not of that shape
	Layers     []Layer   `json:"layers,omitempty"`      // compressed, manifest order
	DiffIDs    []string  `json:"diff_ids,omitempty"`    // the config's rootfs.diff_ids; nil when there are not as many as Layers
}

// Sanitized is p with every fact that is not of the form it claims removed: a
// Claude Code label that is not a version; no layer list unless every size is
// plausible and every digest well formed; no diff ids unless there is one
// well-formed id per layer. Resolve applies it to what a registry said, and the
// cache loader to what a file said, so the two cannot disagree on what is usable.
func (p Platform) Sanitized() Platform {
	if !boximage.ValidClaudeCode(p.ClaudeCode) {
		p.ClaudeCode = ""
	}
	layersOK := !slices.ContainsFunc(p.Layers, func(l Layer) bool {
		return l.Size < 0 || l.Size > maxLayerSize || !digestForm.MatchString(l.Digest)
	})
	switch {
	case !layersOK:
		p.Layers, p.DiffIDs = nil, nil
	case len(p.DiffIDs) != len(p.Layers) || slices.ContainsFunc(p.DiffIDs, func(d string) bool { return !digestForm.MatchString(d) }):
		p.DiffIDs = nil
	}
	return p
}

// Layers and DiffIDs are nil when unusable (an implausible size, a malformed or
// miscounted diff id): the platform still resolves, it just has no download figure.

// Index is a resolved published image: the ref to pull and what each platform
// (keyed by architecture, "amd64" or "arm64") says about it.
type Index struct {
	Ref       string              `json:"ref"`
	Platforms map[string]Platform `json:"platforms"`
}

// Resolve finds the published box image built from the Dockerfile whose Hash is
// hash: the index tagged dockerfile-<hash> in repoPath on registry. It returns
// boximage.PublishedRepo@<index digest> only when the index holds exactly
// wantPlatforms and every platform's image carries the label boximage.LabelHash =
// hash. Every body is hashed and must equal the digest that named it, so nothing
// here trusts the registry's word for what it served. The platform facts come
// from bodies checked that way, and are validated before they are returned.
func Resolve(ctx context.Context, c *http.Client, registry, repoPath, hash string) (Index, error) {
	tag := "dockerfile-" + hash
	token, err := pullToken(ctx, c, registry, repoPath)
	if err != nil {
		return Index{}, err
	}
	get := func(kind, ref, accept string) ([]byte, http.Header, error) {
		return fetch(ctx, c, registry+"/v2/"+repoPath+"/"+kind+"/"+ref, token, accept)
	}
	accept := strings.Join([]string{ociIndex, dockerList, ociManifest, dockerSchema2}, ", ")
	body, hdr, err := get("manifests", tag, accept)
	if err != nil {
		err = fmt.Errorf("%s:%s: %w (box-image.yml publishes it on main when docker/culibox changes; run it, then re-run this release)", repoPath, tag, err)
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusNotFound {
			err = tagMissing{err}
		}
		return Index{}, err
	}
	index := digest(body)
	if h := hdr.Get("Docker-Content-Digest"); h != "" && h != index {
		return Index{}, fmt.Errorf("%s:%s: the registry names it %q, its body hashes to %s: does not match", repoPath, tag, h, index)
	}
	var idx struct {
		MediaType string       `json:"mediaType"`
		Manifests []descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return Index{}, fmt.Errorf("%s:%s: %w", repoPath, tag, err)
	}
	if idx.MediaType != ociIndex && idx.MediaType != dockerList {
		return Index{}, fmt.Errorf("%s:%s is %q, not a multi-arch index", repoPath, tag, idx.MediaType)
	}
	var got []string
	platforms := map[string]Platform{}
	for _, m := range idx.Manifests {
		p := "unknown/unknown"
		if m.Platform != nil {
			p = m.Platform.OS + "/" + m.Platform.Architecture
		}
		if !slices.Contains(wantPlatforms, p) || slices.Contains(got, p) {
			return Index{}, fmt.Errorf("%s:%s holds %q, want exactly %s", repoPath, tag, p, strings.Join(wantPlatforms, " and "))
		}
		got = append(got, p)
		facts, err := checkPlatform(get, m, p, hash)
		if err != nil {
			return Index{}, fmt.Errorf("%s:%s %q: %w", repoPath, tag, p, err)
		}
		platforms[m.Platform.Architecture] = facts
	}
	for _, p := range wantPlatforms {
		if !slices.Contains(got, p) {
			return Index{}, fmt.Errorf("%s:%s lacks %s", repoPath, tag, p)
		}
	}
	ref := boximage.PublishedRepo + "@" + index
	if !boximage.ValidPublished(ref) {
		return Index{}, fmt.Errorf("%s is not a valid published ref", ref)
	}
	return Index{Ref: ref, Platforms: platforms}, nil
}

// checkPlatform fetches one platform's manifest and image config and checks the
// config is that platform's and carries the Dockerfile label hash. It returns
// the platform's layers, diff ids, date and Claude Code label.
func checkPlatform(get func(kind, ref, accept string) ([]byte, http.Header, error), m descriptor, platform, hash string) (Platform, error) {
	if !digestForm.MatchString(m.Digest) {
		return Platform{}, fmt.Errorf("manifest digest %q is not sha256:<64 hex>", m.Digest)
	}
	body, _, err := get("manifests", m.Digest, ociManifest+", "+dockerSchema2)
	if err != nil {
		return Platform{}, err
	}
	if d := digest(body); d != m.Digest {
		return Platform{}, fmt.Errorf("manifest %q hashes to %s: does not match", m.Digest, d)
	}
	var man struct {
		Config descriptor   `json:"config"`
		Layers []descriptor `json:"layers"`
	}
	if err := json.Unmarshal(body, &man); err != nil {
		return Platform{}, err
	}
	if !digestForm.MatchString(man.Config.Digest) {
		return Platform{}, fmt.Errorf("manifest %q names no sha256 config (%q)", m.Digest, man.Config.Digest)
	}
	// Layer digests are only recorded, never fetched, but they reach the cache
	// and the UI, so they get the same form check as the manifests.
	layers := make([]Layer, 0, len(man.Layers))
	for _, l := range man.Layers {
		if !digestForm.MatchString(l.Digest) {
			return Platform{}, fmt.Errorf("manifest %q layer digest %q is not sha256:<64 hex>", m.Digest, l.Digest)
		}
		layers = append(layers, Layer{Digest: l.Digest, Size: l.Size})
	}
	body, _, err = get("blobs", man.Config.Digest, "*/*")
	if err != nil {
		return Platform{}, err
	}
	if d := digest(body); d != man.Config.Digest {
		return Platform{}, fmt.Errorf("config %q hashes to %s: does not match", man.Config.Digest, d)
	}
	var cfg struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Created      string `json:"created"`
		RootFS       struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return Platform{}, err
	}
	if p := cfg.OS + "/" + cfg.Architecture; p != platform {
		return Platform{}, fmt.Errorf("its image config is %q", p)
	}
	if l := cfg.Config.Labels[boximage.LabelHash]; l != hash {
		return Platform{}, fmt.Errorf("label %s is %q, want %s", boximage.LabelHash, l, hash)
	}
	facts := Platform{Layers: layers, DiffIDs: cfg.RootFS.DiffIDs}
	if t, err := time.Parse(time.RFC3339Nano, cfg.Created); err == nil {
		facts.Created = t
	}
	facts.ClaudeCode = cfg.Config.Labels[boximage.LabelClaudeCode]
	// The platform is kept whatever Sanitized drops: an implausible size, a
	// miscounted or malformed diff id only costs it the download figure.
	facts = facts.Sanitized()
	return facts, nil
}

// pullToken is an anonymous pull token for repoPath (ghcr's public packages).
func pullToken(ctx context.Context, c *http.Client, registry, repoPath string) (string, error) {
	q := url.Values{"scope": {"repository:" + repoPath + ":pull"}, "service": {"ghcr.io"}}
	body, _, err := fetch(ctx, c, registry+"/token?"+q.Encode(), "", "application/json")
	if err != nil {
		return "", fmt.Errorf("anonymous pull token: %w", err)
	}
	var t struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.Token == "" {
		return "", fmt.Errorf("anonymous pull token: no token in the answer")
	}
	return t.Token, nil
}

func fetch(ctx context.Context, c *http.Client, u, token, accept string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxBody {
		return nil, nil, fmt.Errorf("GET %s: answer larger than %d bytes", req.URL.Path, maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, &statusError{path: req.URL.Path, status: resp.Status, code: resp.StatusCode}
	}
	return body, resp.Header, nil
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}
