package boximagecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
)

// worldOpts says what the fake ghcr and the fake GitHub API answer.
type worldOpts struct {
	tagMissing bool   // 404 on the dockerfile tag
	issuesCode int    // 0 = 200
	issues     string // the issues answer body
}

// world is one server that plays both ghcr (an index under dockerfile-<Hash()>
// with an amd64 and an arm64 image) and api.github.com's issue list. It records
// the headers of every issues request.
type world struct {
	mu     sync.Mutex
	issues []http.Header
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func fakeWorld(t *testing.T, o worldOpts) (*httptest.Server, *world) {
	t.Helper()
	w := &world{}
	manifests, blobs := map[string][]byte{}, map[string][]byte{}
	type desc struct {
		MediaType string            `json:"mediaType"`
		Digest    string            `json:"digest"`
		Size      int               `json:"size"`
		Platform  map[string]string `json:"platform,omitempty"`
	}
	var children []desc
	for _, arch := range []string{"amd64", "arm64"} {
		layer := digestOf([]byte(arch + "-layer"))
		cfg, _ := json.Marshal(map[string]any{"architecture": arch, "os": "linux", "created": "2026-11-20T08:30:00Z",
			"rootfs": map[string]any{"type": "layers", "diff_ids": []string{digestOf([]byte(arch + "-diff"))}},
			"config": map[string]any{"Labels": map[string]string{boximage.LabelHash: boximage.Hash(), boximage.LabelClaudeCode: "2.1.311"}}})
		blobs[digestOf(cfg)] = cfg
		man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": desc{Digest: digestOf(cfg), Size: len(cfg)},
			"layers": []desc{{Digest: layer, Size: 1_000_000}}})
		manifests[digestOf(man)] = man
		children = append(children, desc{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: digestOf(man),
			Size: len(man), Platform: map[string]string{"os": "linux", "architecture": arch}})
	}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, q *http.Request) {
		path := q.URL.Path
		switch {
		case path == "/token":
			_, _ = rw.Write([]byte(`{"token":"t"}`))
		case path == "/repos/culiops/culibox/issues":
			w.mu.Lock()
			w.issues = append(w.issues, q.Header.Clone())
			w.mu.Unlock()
			if q.Header.Get("Authorization") != "" {
				t.Errorf("the scan request carries Authorization: %q", q.Header.Get("Authorization"))
			}
			for k, want := range map[string]string{"state": "open", "creator": "github-actions[bot]", "per_page": "100"} {
				if got := q.URL.Query().Get(k); got != want {
					t.Errorf("issues query %s = %q, want %q", k, got, want)
				}
			}
			if o.issuesCode != 0 {
				rw.WriteHeader(o.issuesCode)
			}
			_, _ = rw.Write([]byte(o.issues))
		case path == "/v2/culiops/culibox/manifests/dockerfile-"+boximage.Hash():
			if o.tagMissing {
				http.NotFound(rw, q)
				return
			}
			rw.Header().Set("Docker-Content-Digest", digestOf(index))
			_, _ = rw.Write(index)
		case strings.HasPrefix(path, "/v2/culiops/culibox/manifests/"):
			if b, ok := manifests[strings.TrimPrefix(path, "/v2/culiops/culibox/manifests/")]; ok {
				_, _ = rw.Write(b)
				return
			}
			http.NotFound(rw, q)
		case strings.HasPrefix(path, "/v2/culiops/culibox/blobs/"):
			if b, ok := blobs[strings.TrimPrefix(path, "/v2/culiops/culibox/blobs/")]; ok {
				_, _ = rw.Write(b)
				return
			}
			http.NotFound(rw, q)
		default:
			http.NotFound(rw, q)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, w
}

type issue struct {
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	User        map[string]any `json:"user"`
	PullRequest map[string]any `json:"pull_request,omitempty"`
}

const scanPrefix = "Box image: vulnerabilities with a fix"

func botIssue(n int, title string) issue {
	return issue{Number: n, Title: title, User: map[string]any{"login": "github-actions[bot]"}}
}

func issuesBody(is ...issue) string {
	b, _ := json.Marshal(is)
	return string(b)
}

func scanBody(n, count int) string {
	return issuesBody(botIssue(n, fmt.Sprintf("%s: %d HIGH or CRITICAL", scanPrefix, count)))
}

func checkAt(srv *httptest.Server) Checker {
	return Checker{Client: srv.Client(), Registry: srv.URL, GitHubAPI: srv.URL, Now: func() time.Time { return t0 }}
}

func TestCheckHappy(t *testing.T) {
	srv, _ := fakeWorld(t, worldOpts{issues: scanBody(357, 680)})
	c := checkAt(srv).Check(context.Background())
	if c == nil || c.Version != 1 || !c.CheckedAt.Equal(t0) || c.Hash != boximage.Hash() {
		t.Fatalf("header: %+v", c)
	}
	if c.RegistryErr != "" || c.Registry == nil || !boximage.ValidPublished(c.Registry.Ref) || c.Registry.Platforms["arm64"].ClaudeCode != "2.1.311" {
		t.Fatalf("registry: %+v / %q", c.Registry, c.RegistryErr)
	}
	if c.ScanErr != "" || c.Scan == nil || *c.Scan != (Scan{357, 680}) {
		t.Fatalf("scan: %+v / %q", c.Scan, c.ScanErr)
	}
}

func TestCheckRegistryDown(t *testing.T) {
	// closed server: nothing answers
	dead := httptest.NewServer(http.NotFoundHandler())
	k := checkAt(dead)
	dead.Close()
	c := k.Check(context.Background())
	if c == nil || c.RegistryErr != "registry unreachable" || c.Registry != nil {
		t.Fatalf("closed: %+v", c)
	}
	if c.ScanErr != "scan unreadable" || c.Scan != nil {
		t.Errorf("closed, scan: %+v / %q", c.Scan, c.ScanErr)
	}

	srv, _ := fakeWorld(t, worldOpts{tagMissing: true, issues: scanBody(1, 2)})
	c = checkAt(srv).Check(context.Background())
	if c.RegistryErr != "not published yet" || c.Registry != nil {
		t.Errorf("404 tag: %+v / %q", c.Registry, c.RegistryErr)
	}
	if c.Scan == nil || *c.Scan != (Scan{1, 2}) {
		t.Errorf("a missing tag must not stop the scan: %+v", c.Scan)
	}
}

func TestCheckUnexpectedRegistryAnswer(t *testing.T) {
	// the token endpoint answers, but with a body that is no token
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/token" {
			http.Error(w, "<html>boom</html>", http.StatusBadGateway)
			return
		}
		http.NotFound(w, q)
	}))
	defer srv.Close()
	c := checkAt(srv).Check(context.Background())
	if c.RegistryErr != "unexpected registry answer" || c.Registry != nil {
		t.Fatalf("%+v / %q", c.Registry, c.RegistryErr)
	}
	if strings.Contains(c.RegistryErr, "boom") {
		t.Error("a raw body reached the cache")
	}
}

func TestCheckScanUnreadable(t *testing.T) {
	for name, o := range map[string]worldOpts{
		"403":         {issuesCode: http.StatusForbidden, issues: `{"message":"rate limit exceeded"}`},
		"not json":    {issues: `<html>`},
		"not a list":  {issues: `{"message":"x"}`},
		"empty body":  {issues: ``},
		"server fail": {issuesCode: 500},
	} {
		srv, _ := fakeWorld(t, o)
		c := checkAt(srv).Check(context.Background())
		if c.ScanErr != "scan unreadable" || c.Scan != nil {
			t.Errorf("%s: %+v / %q", name, c.Scan, c.ScanErr)
		}
		if c.RegistryErr != "" || c.Registry == nil {
			t.Errorf("%s: the registry part was hurt: %q", name, c.RegistryErr)
		}
	}
}

func TestScanIgnoresHumanAndJunk(t *testing.T) {
	human := botIssue(10, scanPrefix+": 5 HIGH or CRITICAL")
	human.User = map[string]any{"login": "chiplonton"}
	pr := botIssue(11, scanPrefix+": 6 HIGH or CRITICAL")
	pr.PullRequest = map[string]any{"url": "x"}
	noUser := issue{Number: 12, Title: scanPrefix + ": 7 HIGH or CRITICAL"}
	for name, is := range map[string][]issue{
		"human author":    {human},
		"pull request":    {pr},
		"no user":         {noUser},
		"seven digits":    {botIssue(20, scanPrefix+": 9999999 HIGH or CRITICAL")},
		"not a number":    {botIssue(21, scanPrefix+": lots HIGH or CRITICAL")},
		"negative":        {botIssue(22, scanPrefix+": -3 HIGH or CRITICAL")},
		"trailing text":   {botIssue(23, scanPrefix+": 5 HIGH or CRITICAL (old)")},
		"other title":     {botIssue(24, "Weekly report: 5 HIGH or CRITICAL")},
		"zero number":     {botIssue(0, scanPrefix+": 5 HIGH or CRITICAL")},
		"huge number":     {botIssue(1_000_000, scanPrefix+": 5 HIGH or CRITICAL")},
		"no issues":       nil,
		"only the others": {human, botIssue(24, "Weekly report: 5 HIGH or CRITICAL")},
	} {
		srv, _ := fakeWorld(t, worldOpts{issues: issuesBody(is...)})
		c := checkAt(srv).Check(context.Background())
		if c.ScanErr != "" || c.Scan == nil || *c.Scan != (Scan{}) {
			t.Errorf("%s: scan %+v / %q, want {0 0} and no error (none open)", name, c.Scan, c.ScanErr)
		}
	}
	// the bot's issue is found among the others, and the first match wins
	srv, _ := fakeWorld(t, worldOpts{issues: issuesBody(human, botIssue(30, scanPrefix+": lots HIGH or CRITICAL"),
		botIssue(31, scanPrefix+": 12 HIGH or CRITICAL"), botIssue(32, scanPrefix+": 99 HIGH or CRITICAL"))})
	if c := checkAt(srv).Check(context.Background()); c.Scan == nil || *c.Scan != (Scan{31, 12}) {
		t.Fatalf("got %+v", c.Scan)
	}
	// zero findings can be stated by an open issue too
	srv, _ = fakeWorld(t, worldOpts{issues: issuesBody(botIssue(40, scanPrefix+": 0 HIGH or CRITICAL"))})
	if c := checkAt(srv).Check(context.Background()); c.Scan == nil || *c.Scan != (Scan{40, 0}) {
		t.Fatalf("got %+v", c.Scan)
	}
}

func TestScanNoAuthHeader(t *testing.T) {
	srv, w := fakeWorld(t, worldOpts{issues: scanBody(357, 680)}) // fails the test itself if Authorization is present
	checkAt(srv).Check(context.Background())
	if len(w.issues) != 1 {
		t.Fatalf("%d scan requests", len(w.issues))
	}
	h := w.issues[0]
	if h.Get("Authorization") != "" {
		t.Error("Authorization sent")
	}
	for k, want := range map[string]string{"Accept": "application/vnd.github+json", "X-Github-Api-Version": "2022-11-28", "User-Agent": "culibox-image-check"} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// An answer over the cap is refused, not read to the end.
func TestScanBodyCapped(t *testing.T) {
	big := `[{"number":1,"title":"` + strings.Repeat("x", 5<<20) + `"}]`
	srv, _ := fakeWorld(t, worldOpts{issues: big})
	if c := checkAt(srv).Check(context.Background()); c.ScanErr != "scan unreadable" || c.Scan != nil {
		t.Fatalf("%+v / %q", c.Scan, c.ScanErr)
	}
}
