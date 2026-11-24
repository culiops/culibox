package boxregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	boximage "github.com/culiops/culibox/docker/culibox"
)

// maxBody caps every registry response: an index, a manifest or an image config
// is a few KB.
const maxBody = 4 << 20

const (
	ociIndex      = "application/vnd.oci.image.index.v1+json"
	dockerList    = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociManifest   = "application/vnd.oci.image.manifest.v1+json"
	dockerSchema2 = "application/vnd.docker.distribution.manifest.v2+json"
)

// digestForm is the only digest form fetched: it becomes part of a request path
// and of error messages, so nothing else the registry names may get that far.
var digestForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// wantPlatforms are the arches box-image.yml publishes, and all an index may hold.
var wantPlatforms = []string{"linux/amd64", "linux/arm64"}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

// Strings the registry supplies are printed with %q: this runs in a GitHub
// Actions step, which reads "::" commands from a line's start.
//
// Resolve finds the published box image built from the Dockerfile whose Hash is
// hash: the index tagged dockerfile-<hash> in repoPath on registry. It returns
// boximage.PublishedRepo@<index digest> only when the index holds exactly
// wantPlatforms and every platform's image carries the label boximage.LabelHash =
// hash. Every body is hashed and must equal the digest that named it, so nothing
// here trusts the registry's word for what it served.
func Resolve(ctx context.Context, c *http.Client, registry, repoPath, hash string) (string, error) {
	tag := "dockerfile-" + hash
	token, err := pullToken(ctx, c, registry, repoPath)
	if err != nil {
		return "", err
	}
	get := func(kind, ref, accept string) ([]byte, http.Header, error) {
		return fetch(ctx, c, registry+"/v2/"+repoPath+"/"+kind+"/"+ref, token, accept)
	}
	accept := strings.Join([]string{ociIndex, dockerList, ociManifest, dockerSchema2}, ", ")
	body, hdr, err := get("manifests", tag, accept)
	if err != nil {
		return "", fmt.Errorf("%s:%s: %w (box-image.yml publishes it on main when docker/culibox changes; run it, then re-run this release)", repoPath, tag, err)
	}
	index := digest(body)
	if h := hdr.Get("Docker-Content-Digest"); h != "" && h != index {
		return "", fmt.Errorf("%s:%s: the registry names it %q, its body hashes to %s: does not match", repoPath, tag, h, index)
	}
	var idx struct {
		MediaType string       `json:"mediaType"`
		Manifests []descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return "", fmt.Errorf("%s:%s: %w", repoPath, tag, err)
	}
	if idx.MediaType != ociIndex && idx.MediaType != dockerList {
		return "", fmt.Errorf("%s:%s is %q, not a multi-arch index", repoPath, tag, idx.MediaType)
	}
	var got []string
	for _, m := range idx.Manifests {
		p := "unknown/unknown"
		if m.Platform != nil {
			p = m.Platform.OS + "/" + m.Platform.Architecture
		}
		if !slices.Contains(wantPlatforms, p) || slices.Contains(got, p) {
			return "", fmt.Errorf("%s:%s holds %q, want exactly %s", repoPath, tag, p, strings.Join(wantPlatforms, " and "))
		}
		got = append(got, p)
		if err := checkPlatform(get, m, p, hash); err != nil {
			return "", fmt.Errorf("%s:%s %q: %w", repoPath, tag, p, err)
		}
	}
	for _, p := range wantPlatforms {
		if !slices.Contains(got, p) {
			return "", fmt.Errorf("%s:%s lacks %s", repoPath, tag, p)
		}
	}
	ref := boximage.PublishedRepo + "@" + index
	if !boximage.ValidPublished(ref) {
		return "", fmt.Errorf("%s is not a valid published ref", ref)
	}
	return ref, nil
}

// checkPlatform fetches one platform's manifest and image config and checks the
// config is that platform's and carries the Dockerfile label hash.
func checkPlatform(get func(kind, ref, accept string) ([]byte, http.Header, error), m descriptor, platform, hash string) error {
	if !digestForm.MatchString(m.Digest) {
		return fmt.Errorf("manifest digest %q is not sha256:<64 hex>", m.Digest)
	}
	body, _, err := get("manifests", m.Digest, ociManifest+", "+dockerSchema2)
	if err != nil {
		return err
	}
	if d := digest(body); d != m.Digest {
		return fmt.Errorf("manifest %q hashes to %s: does not match", m.Digest, d)
	}
	var man struct {
		Config descriptor `json:"config"`
	}
	if err := json.Unmarshal(body, &man); err != nil {
		return err
	}
	if !digestForm.MatchString(man.Config.Digest) {
		return fmt.Errorf("manifest %q names no sha256 config (%q)", m.Digest, man.Config.Digest)
	}
	body, _, err = get("blobs", man.Config.Digest, "*/*")
	if err != nil {
		return err
	}
	if d := digest(body); d != man.Config.Digest {
		return fmt.Errorf("config %q hashes to %s: does not match", man.Config.Digest, d)
	}
	var cfg struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Config       struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return err
	}
	if p := cfg.OS + "/" + cfg.Architecture; p != platform {
		return fmt.Errorf("its image config is %q", p)
	}
	if l := cfg.Config.Labels[boximage.LabelHash]; l != hash {
		return fmt.Errorf("label %s is %q, want %s", boximage.LabelHash, l, hash)
	}
	return nil
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
		return nil, nil, fmt.Errorf("GET %s: %s", req.URL.Path, resp.Status)
	}
	return body, resp.Header, nil
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}
