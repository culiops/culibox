package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	boximage "github.com/culiops/culibox/docker/agent-box"
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

func newRegistry(t *testing.T, plats ...platform) *registry {
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
		cfg, _ := json.Marshal(map[string]any{"architecture": p.arch, "os": p.os,
			"config": map[string]any{"Labels": map[string]string{boximage.LabelHash: p.label}}})
		r.blobs[digestOf(cfg)] = cfg
		man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": desc{MediaType: "application/vnd.oci.image.config.v1+json", Digest: digestOf(cfg), Size: len(cfg)}})
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

func resolveFrom(t *testing.T, r *registry) (string, error) {
	t.Helper()
	srv := r.serve()
	return resolve(context.Background(), srv.Client(), srv.URL, "culiops/culibox", testHash)
}

func TestResolveHappy(t *testing.T) {
	r := newRegistry(t, both(testHash)...)
	got, err := resolveFrom(t, r)
	if err != nil {
		t.Fatal(err)
	}
	if want := boximage.PublishedRepo + "@" + digestOf(r.index); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !boximage.ValidPublished(got) {
		t.Fatalf("%q is not a valid published ref", got)
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
