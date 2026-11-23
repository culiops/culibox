package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// A body that compresses well, so the gzip copy is kept (a copy that is not smaller is dropped).
var bigJS = strings.Repeat("function f() { return 1; }\n", 200)

func testAssets() *staticAssets {
	return newStaticAssets(fstest.MapFS{
		"app.js":                     {Data: []byte(bigJS)},
		"app.css":                    {Data: []byte(strings.Repeat(".a { color: red; }\n", 200))},
		"font.woff2":                 {Data: []byte(strings.Repeat("\x00\x01woff", 400))},
		"img.png":                    {Data: []byte(strings.Repeat("\x89PNG", 400))},
		"vendor/marked.min.js":       {Data: []byte(bigJS + "// marked\n")},
		"vendor/sub/inner.js":        {Data: []byte("x")},
		"xterm/xterm.js":             {Data: []byte(bigJS + "// xterm\n")},
		"tiny.js":                    {Data: []byte("x")},
		"index.html":                 {Data: []byte(`<head><meta name="culibox-asset-base" content=""><link rel="stylesheet" href="/vendor/a.css"><link rel="stylesheet" href="/app.css"></head><script src="/xterm/xterm.js"></script><script src="/vendor/marked.min.js"></script><script src="/app.js"></script>`)},
		"vendor/a.css":               {Data: []byte(".a{}")},
		"vendor/monaco/vs/loader.js": {Data: []byte(bigJS + "// loader\n")},
	})
}

func serveAsset(a *staticAssets, name, cc string, hdr map[string]string) (*httptest.ResponseRecorder, bool) {
	req := httptest.NewRequest("GET", "/"+name, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ok := a.serve(rec, req, name, cc)
	return rec, ok
}

var etagRE = regexp.MustCompile(`^"[0-9a-f]{16}(-gz)?"$`)

func TestStaticGzipWhenAccepted(t *testing.T) {
	a := testAssets()
	rec, ok := serveAsset(a, "app.js", "no-cache", map[string]string{"Accept-Encoding": "gzip, br"})
	if !ok || rec.Code != 200 {
		t.Fatalf("ok=%v code=%d", ok, rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary=%q", got)
	}
	if et := rec.Header().Get("ETag"); !etagRE.MatchString(et) || !strings.HasSuffix(et, `-gz"`) {
		t.Fatalf("ETag=%q, want a strong -gz tag", et)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("Content-Type=%q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control=%q", cc)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if string(body) != bigJS {
		t.Fatal("gunzipped body is not the file")
	}
}

func TestStaticIdentityWithoutGzip(t *testing.T) {
	a := testAssets()
	for _, ae := range []string{"", "br", "gzip;q=0", "identity", "x-gzip-not"} {
		rec, _ := serveAsset(a, "app.js", "", map[string]string{"Accept-Encoding": ae})
		if rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("Accept-Encoding %q: got Content-Encoding %q", ae, rec.Header().Get("Content-Encoding"))
		}
		if rec.Body.String() != bigJS {
			t.Fatalf("Accept-Encoding %q: body is not the raw file", ae)
		}
		if rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("Accept-Encoding %q: Vary missing", ae)
		}
		if et := rec.Header().Get("ETag"); !etagRE.MatchString(et) || strings.HasSuffix(et, `-gz"`) {
			t.Fatalf("ETag=%q, want the identity tag", et)
		}
		if rec.Header().Get("Cache-Control") != "" {
			t.Fatalf("no Cache-Control asked, got %q", rec.Header().Get("Cache-Control"))
		}
	}
	// gzip with a q above 0 still counts, whatever the spacing and case.
	rec, _ := serveAsset(a, "app.js", "", map[string]string{"Accept-Encoding": "deflate, GZIP ; q=0.5"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("gzip;q=0.5 should be served gzip")
	}
}

func TestStaticNotCompressible(t *testing.T) {
	a := testAssets()
	for _, n := range []string{"font.woff2", "img.png"} {
		rec, ok := serveAsset(a, n, "", map[string]string{"Accept-Encoding": "gzip"})
		if !ok || rec.Code != 200 {
			t.Fatalf("%s: ok=%v code=%d", n, ok, rec.Code)
		}
		if rec.Header().Get("Content-Encoding") != "" || rec.Header().Get("Vary") != "" {
			t.Fatalf("%s: compressed or Vary set (%q, %q)", n, rec.Header().Get("Content-Encoding"), rec.Header().Get("Vary"))
		}
		if !etagRE.MatchString(rec.Header().Get("ETag")) {
			t.Fatalf("%s: ETag=%q", n, rec.Header().Get("ETag"))
		}
	}
	// A compressible file whose gzip copy is not smaller is served as is.
	rec, _ := serveAsset(a, "tiny.js", "", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "x" {
		t.Fatal("tiny.js should be served raw")
	}
}

func TestStatic304(t *testing.T) {
	a := testAssets()
	gz := map[string]string{"Accept-Encoding": "gzip"}
	first, _ := serveAsset(a, "app.js", "no-cache", gz)
	tag := first.Header().Get("ETag")
	rec, _ := serveAsset(a, "app.js", "no-cache", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": tag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation: code=%d body=%d, want 304 empty", rec.Code, rec.Body.Len())
	}
	// The gzip tag does not validate an identity answer, nor the identity tag a gzip one.
	rec, _ = serveAsset(a, "app.js", "", map[string]string{"If-None-Match": tag})
	if rec.Code != 200 {
		t.Fatalf("gzip tag on an identity request: code=%d, want 200", rec.Code)
	}
	idTag := rec.Header().Get("ETag")
	rec, _ = serveAsset(a, "app.js", "", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": idTag})
	if rec.Code != 200 {
		t.Fatalf("identity tag on a gzip request: code=%d, want 200", rec.Code)
	}
}

func TestStaticGzipComputedOnce(t *testing.T) {
	a := testAssets()
	for i := 0; i < 3; i++ {
		serveAsset(a, "app.js", "", map[string]string{"Accept-Encoding": "gzip"})
		serveAsset(a, "app.js", "", nil)
	}
	if a.gzips != 1 {
		t.Fatalf("gzip ran %d times, want 1", a.gzips)
	}
}

func TestStaticDirFallsBack(t *testing.T) {
	a := testAssets()
	for _, n := range []string{"vendor", "vendor/sub", "nope.js", ""} {
		if _, ok := serveAsset(a, n, "", nil); ok {
			t.Fatalf("%q: served, want fall back", n)
		}
	}
}

func TestStaticBuildIDChangesWithAnyFile(t *testing.T) {
	a := testAssets()
	b := newStaticAssets(fstest.MapFS{"app.js": {Data: []byte("1")}})
	c := newStaticAssets(fstest.MapFS{"app.js": {Data: []byte("2")}})
	d := newStaticAssets(fstest.MapFS{"app2.js": {Data: []byte("1")}})
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(a.buildID()) {
		t.Fatalf("buildID=%q", a.buildID())
	}
	if b.buildID() == c.buildID() || b.buildID() == d.buildID() {
		t.Fatal("a changed content or name must change the build id")
	}
	if b.buildID() != newStaticAssets(fstest.MapFS{"app.js": {Data: []byte("1")}}).buildID() {
		t.Fatal("the same tree must give the same id")
	}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}
