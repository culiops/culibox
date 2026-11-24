package boxregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
)

const testHash = "0123456789ab"

type platform struct{ os, arch, label string }

// registry is a fake ghcr: one index under tag dockerfile-<testHash>, its
// per-platform manifests and config blobs. Blobs answer with a redirect, as ghcr
// does. The mutators let a test break one thing.
type registry struct {
	t         *testing.T
	manifests map[string][]byte // by digest
	blobs     map[string][]byte // by digest
	index     []byte
	indexType string
	headerDig string // Docker-Content-Digest for the index; "" = the true one
	tokenCode int
	missing   bool
	corrupt   string // a digest whose body is served altered
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// fixture is what one platform's manifest and config say beyond the label hash.
type fixture struct {
	created string
	claude  string
	layers  []layerFix
	diffIDs []string
}

type layerFix struct {
	digest string
	size   int64
}

const fixtureCreated = "2026-11-20T08:30:00.123456789Z"

// defaultFixture gives each arch its own two layers, so a test can tell the
// platforms apart.
func defaultFixture(arch string) fixture {
	return fixture{
		created: fixtureCreated,
		claude:  "2.1.311",
		layers: []layerFix{
			{digestOf([]byte(arch + "-layer-0")), 29_000_000},
			{digestOf([]byte(arch + "-layer-1")), 1_500_000},
		},
		diffIDs: []string{digestOf([]byte(arch + "-diff-0")), digestOf([]byte(arch + "-diff-1"))},
	}
}

func newRegistry(t *testing.T, plats ...platform) *registry {
	return newRegistryWith(t, nil, plats...)
}

// newRegistryWith lets edit change one platform's fixture before it is served.
func newRegistryWith(t *testing.T, edit func(arch string, f *fixture), plats ...platform) *registry {
	r := &registry{t: t, manifests: map[string][]byte{}, blobs: map[string][]byte{}, tokenCode: 200,
		indexType: "application/vnd.oci.image.index.v1+json"}
	type desc struct {
		MediaType string            `json:"mediaType"`
		Digest    string            `json:"digest"`
		Size      int               `json:"size"`
		Platform  map[string]string `json:"platform,omitempty"`
	}
	var children []desc
	for _, p := range plats {
		f := defaultFixture(p.arch)
		if edit != nil {
			edit(p.arch, &f)
		}
		cfg, _ := json.Marshal(map[string]any{"architecture": p.arch, "os": p.os, "created": f.created,
			"rootfs": map[string]any{"type": "layers", "diff_ids": f.diffIDs},
			"config": map[string]any{"Labels": map[string]string{boximage.LabelHash: p.label, boximage.LabelClaudeCode: f.claude}}})
		r.blobs[digestOf(cfg)] = cfg
		var layers []desc
		for _, l := range f.layers {
			layers = append(layers, desc{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: l.digest, Size: int(l.size)})
		}
		man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": desc{MediaType: "application/vnd.oci.image.config.v1+json", Digest: digestOf(cfg), Size: len(cfg)},
			"layers": layers})
		r.manifests[digestOf(man)] = man
		children = append(children, desc{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: digestOf(man),
			Size: len(man), Platform: map[string]string{"os": p.os, "architecture": p.arch}})
	}
	r.index, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": r.indexType, "manifests": children})
	return r
}

func (r *registry) serve() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		switch {
		case q.URL.Path == "/token":
			if got := q.URL.Query().Get("scope"); got != "repository:culiops/culibox:pull" {
				r.t.Errorf("token scope %q", got)
			}
			w.WriteHeader(r.tokenCode)
			_, _ = w.Write([]byte(`{"token":"t"}`))
			return
		case strings.HasPrefix(q.URL.Path, "/cdn/"):
			r.write(w, strings.TrimPrefix(q.URL.Path, "/cdn/"), r.blobs[strings.TrimPrefix(q.URL.Path, "/cdn/")])
			return
		}
		if q.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if ref, ok := strings.CutPrefix(q.URL.Path, "/v2/culiops/culibox/manifests/"); ok {
			if ref == "dockerfile-"+testHash {
				// The tag must be asked for as an index, or ghcr answers with one arch.
				accept := q.Header.Get("Accept")
				for _, mt := range []string{"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json"} {
					if !strings.Contains(accept, mt) {
						r.t.Errorf("Accept %q lacks %s", accept, mt)
					}
				}
				if r.missing {
					http.NotFound(w, q)
					return
				}
				dig := r.headerDig
				if dig == "" {
					dig = digestOf(r.index)
				}
				w.Header().Set("Docker-Content-Digest", dig)
				r.write(w, digestOf(r.index), r.index)
				return
			}
			if b, ok := r.manifests[ref]; ok {
				r.write(w, ref, b)
				return
			}
			http.NotFound(w, q)
			return
		}
		if dig, ok := strings.CutPrefix(q.URL.Path, "/v2/culiops/culibox/blobs/"); ok {
			if _, ok := r.blobs[dig]; !ok {
				http.NotFound(w, q)
				return
			}
			http.Redirect(w, q, "/cdn/"+dig, http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, q)
	}))
	r.t.Cleanup(srv.Close)
	return srv
}

func (r *registry) write(w http.ResponseWriter, dig string, b []byte) {
	if dig == r.corrupt {
		b = append(append([]byte(nil), b...), ' ')
	}
	_, _ = w.Write(b)
}

func both(label string) []platform {
	return []platform{{"linux", "amd64", label}, {"linux", "arm64", label}}
}

func resolveFrom(t *testing.T, r *registry) (Index, error) {
	t.Helper()
	srv := r.serve()
	return Resolve(context.Background(), srv.Client(), srv.URL, "culiops/culibox", testHash)
}

func TestResolveHappy(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	got, err := resolveFrom(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if want := boximage.PublishedRepo + "@" + digestOf(r.index); got.Ref != want {
		t.Fatalf("got %q, want %q", got.Ref, want)
	}
	if !boximage.ValidPublished(got.Ref) {
		t.Fatalf("%q is not a valid published ref", got.Ref)
	}
}

func TestResolvePlatformFacts(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	got, err := resolveFrom(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Platforms) != 2 {
		t.Fatalf("platforms = %v, want amd64 and arm64", got.Platforms)
	}
	wantCreated, err := time.Parse(time.RFC3339Nano, fixtureCreated)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		p, ok := got.Platforms[arch]
		if !ok {
			t.Fatalf("no %s platform in %v", arch, got.Platforms)
		}
		want := defaultFixture(arch)
		if p.ClaudeCode != "2.1.311" {
			t.Errorf("%s ClaudeCode = %q", arch, p.ClaudeCode)
		}
		if !p.Created.Equal(wantCreated) {
			t.Errorf("%s Created = %v, want %v", arch, p.Created, wantCreated)
		}
		var wantLayers []Layer
		for _, l := range want.layers {
			wantLayers = append(wantLayers, Layer{Digest: l.digest, Size: l.size})
		}
		if !slices.Equal(p.Layers, wantLayers) {
			t.Errorf("%s Layers = %v, want %v", arch, p.Layers, wantLayers)
		}
		if !slices.Equal(p.DiffIDs, want.diffIDs) {
			t.Errorf("%s DiffIDs = %v, want %v", arch, p.DiffIDs, want.diffIDs)
		}
	}
	if got.Platforms["amd64"].Layers[0] == got.Platforms["arm64"].Layers[0] {
		t.Error("the two platforms share a layer: facts are not read per platform")
	}
}

// A config whose diff_ids do not line up with the manifest's layers cannot give
// a download size, but it is still a valid image.
func TestResolveLayerCountMismatch(t *testing.T) {
	r := newRegistryWith(t, func(arch string, f *fixture) {
		if arch == "arm64" {
			f.diffIDs = f.diffIDs[:len(f.diffIDs)-1]
		}
	}, both(testHash)...)
	got, err := resolveFrom(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if p := got.Platforms["arm64"]; p.DiffIDs != nil || len(p.Layers) != 2 {
		t.Errorf("arm64 = %+v, want DiffIDs nil and its 2 layers", p)
	}
	if p := got.Platforms["amd64"]; len(p.DiffIDs) != 2 {
		t.Errorf("amd64 DiffIDs = %v, want the 2 it published", p.DiffIDs)
	}
}

func TestResolveDropsInvalidLabels(t *testing.T) {
	r := newRegistryWith(t, func(_ string, f *fixture) {
		f.claude = "2.1.311; rm -rf"
		f.created = "yesterday"
	}, both(testHash)...)
	got, err := resolveFrom(t, r)
	if err != nil {
		t.Fatal(err)
	}
	for arch, p := range got.Platforms {
		if p.ClaudeCode != "" {
			t.Errorf("%s ClaudeCode = %q, want it dropped", arch, p.ClaudeCode)
		}
		if !p.Created.IsZero() {
			t.Errorf("%s Created = %v, want the zero time", arch, p.Created)
		}
	}
}

// A layer digest is held to the form manifests are: it is read into the cache
// and may be shown, so nothing else gets through.
func TestResolveRejectsMalformedLayerDigest(t *testing.T) {
	r := newRegistryWith(t, func(_ string, f *fixture) {
		f.layers[0].digest = "sha256:ab\n::warning::x"
	}, both(testHash)...)
	_, err := resolveFrom(t, r)
	if err == nil || !strings.Contains(err.Error(), "not sha256:<64 hex>") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveMissingTag(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	r.missing = true
	_, err := resolveFrom(t, r)
	if err == nil || !strings.Contains(err.Error(), "dockerfile-"+testHash) || !strings.Contains(err.Error(), "box-image") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveWrongLabel(t *testing.T) {
	r := newRegistry(t, platform{"linux", "amd64", testHash}, platform{"linux", "arm64", "ffffffffffff"})
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "ffffffffffff") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveMissingArch(t *testing.T) {
	r := newRegistry(t, platform{"linux", "amd64", testHash})
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveExtraPlatform(t *testing.T) {
	r := newRegistry(t, append(both(testHash), platform{"unknown", "unknown", ""})...)
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "unknown/unknown") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveDuplicatePlatform(t *testing.T) {
	r := newRegistry(t, append(both(testHash), platform{"linux", "amd64", testHash})...)
	if _, err := resolveFrom(t, r); err == nil {
		t.Fatal("an index with linux/amd64 twice was accepted")
	}
}

func TestResolveManifestDigestMismatch(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	for d := range r.manifests {
		r.corrupt = d
		break
	}
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveBlobDigestMismatch(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	for d := range r.blobs {
		r.corrupt = d
		break
	}
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveIndexHeaderMismatch(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	r.headerDig = "sha256:" + strings.Repeat("0", 64)
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveTokenRefused(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	r.tokenCode = http.StatusUnauthorized
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v", err)
	}
}

// A single-arch manifest under the tag is not an index.
func TestResolveRejectsNonIndex(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	for _, m := range r.manifests {
		r.index = m
		break
	}
	if _, err := resolveFrom(t, r); err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("err = %v", err)
	}
}

// A digest the index names reaches a request path and error text only in the
// sha256:<64 hex> form (no newline can reach the Actions log).
func TestResolveRejectsMalformedDigest(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	r.index = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:ab\n::warning::x","platform":{"os":"linux","architecture":"amd64"}}]}`)
	_, err := resolveFrom(t, r)
	if err == nil || !strings.Contains(err.Error(), "not sha256:<64 hex>") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
}
