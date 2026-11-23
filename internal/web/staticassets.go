package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// staticAssets serves the embedded static tree with what http.FileServer over an embed.FS cannot give: embed.FS has no
// modification time, so FileServer sends no Last-Modified and no ETag, nothing can revalidate, and every reload downloads
// every file again (measured: 220 requests, 9.5 MB per reload). Here each file gets a strong ETag from its content hash
// (a reload revalidates with 304), and a compressible file is gzipped once per process, on its first request, and held in
// memory. The build id names the whole tree, so a URL carrying it changes whenever any file does (versioned URLs, web.go).
type staticAssets struct {
	fsys fs.FS

	idOnce sync.Once
	id     string

	indexOnce  sync.Once
	indexEntry staticEntry // index.html as rewritten for this build (serveIndex)

	mu      sync.Mutex
	entries map[string]*staticEntry
	gzips   int // how many gzip copies were made (a test checks each is made once)
}

type staticEntry struct {
	once  sync.Once
	ok    bool // a regular file that could be read
	raw   []byte
	gz    []byte // nil when the type is not compressible or the copy is not smaller
	tag   string // the 16-hex content hash, without quotes
	ctype string
}

// staticCompressible: the types worth gzipping. Fonts, images and the like are already compressed.
var staticCompressible = map[string]bool{".js": true, ".css": true, ".html": true, ".json": true, ".svg": true, ".map": true}

func newStaticAssets(fsys fs.FS) *staticAssets {
	return &staticAssets{fsys: fsys, entries: map[string]*staticEntry{}}
}

// buildID is the first 16 hex characters of a SHA-256 over every regular file's path and content, in WalkDir's lexical
// order: a changed, added, removed or renamed file gives another id.
func (a *staticAssets) buildID() string {
	a.idOnce.Do(func() {
		h := sha256.New()
		var n [8]byte
		_ = fs.WalkDir(a.fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			b, err := fs.ReadFile(a.fsys, p)
			if err != nil {
				return nil
			}
			h.Write([]byte(p))
			h.Write([]byte{0})
			binary.BigEndian.PutUint64(n[:], uint64(len(b)))
			h.Write(n[:])
			h.Write(b)
			return nil
		})
		a.id = hex.EncodeToString(h.Sum(nil))[:16]
	})
	return a.id
}

// entry returns the file's entry, reading and compressing it the first time it is asked for.
func (a *staticAssets) entry(name string) *staticEntry {
	a.mu.Lock()
	e := a.entries[name]
	if e == nil {
		e = &staticEntry{}
		a.entries[name] = e
	}
	a.mu.Unlock()
	e.once.Do(func() { a.load(e, name) })
	if !e.ok {
		// Not a file (a directory, a miss): nothing is kept for it, so asking for ever new paths cannot grow the map.
		a.mu.Lock()
		if a.entries[name] == e {
			delete(a.entries, name)
		}
		a.mu.Unlock()
	}
	return e
}

func (a *staticAssets) load(e *staticEntry, name string) {
	if name == "" || !fs.ValidPath(name) {
		return
	}
	st, err := fs.Stat(a.fsys, name)
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	raw, err := fs.ReadFile(a.fsys, name)
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	e.raw, e.tag, e.ok = raw, hex.EncodeToString(sum[:])[:16], true
	ext := strings.ToLower(path.Ext(name))
	e.ctype = mime.TypeByExtension(ext)
	if staticCompressible[ext] {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		if buf.Len() < len(raw) {
			e.gz = buf.Bytes()
		}
		a.mu.Lock()
		a.gzips++
		a.mu.Unlock()
	}
}

// serve answers GET/HEAD for the regular file `name` (a path inside the tree, no leading slash), with `cacheControl` when
// it is not empty, and reports whether it did: false (a directory, a missing file) leaves the request to the caller.
func (a *staticAssets) serve(w http.ResponseWriter, r *http.Request, name, cacheControl string) bool {
	e := a.entry(name)
	if !e.ok {
		return false
	}
	a.write(w, r, name, e.raw, e.gz, e.tag, e.ctype, cacheControl)
	return true
}

// write sends one representation: the gzip copy when there is one and the client takes gzip, else the raw bytes. The two
// carry different strong ETags, so one never validates the other. ServeContent answers If-None-Match with 304.
func (a *staticAssets) write(w http.ResponseWriter, r *http.Request, name string, raw, gz []byte, tag, ctype, cacheControl string) {
	h := w.Header()
	body := raw
	if staticCompressible[strings.ToLower(path.Ext(name))] {
		h.Set("Vary", "Accept-Encoding")
	}
	if gz != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body = gz
		tag += "-gz"
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("ETag", `"`+tag+`"`)
	if ctype != "" {
		h.Set("Content-Type", ctype) // set, so ServeContent never sniffs the gzip bytes
	}
	if cacheControl != "" {
		h.Set("Cache-Control", cacheControl)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
}

// acceptsGzip: the Accept-Encoding header names gzip with a q above 0.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(token), "gzip") {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		return q > 0
	}
	return false
}

// staticImmutable: what a versioned URL is cached for. The URL names the build, so its content never changes. private: the
// answer needs a login, so no shared cache in front of the server keeps it.
const staticImmutable = "private, max-age=31536000, immutable"

// assetBaseMeta is the tag index.html carries for the page's scripts (monacoload.js reads it) to learn the versioned base.
const assetBaseMeta = `<meta name="culibox-asset-base" content="">`

// rewriteIndex points index.html's vendor and xterm URLs (attribute values starting "/vendor/" or "/xterm/") at `base`
// ("/v/<build id>"), and fills the asset-base meta tag with it. Nothing else changes: app scripts and styles keep their
// unversioned, revalidated URLs (they change with every build, and /profileform.js is generated per request).
func rewriteIndex(html []byte, base string) []byte {
	out := bytes.ReplaceAll(html, []byte(`="/vendor/`), []byte(`="`+base+`/vendor/`))
	out = bytes.ReplaceAll(out, []byte(`="/xterm/`), []byte(`="`+base+`/xterm/`))
	return bytes.Replace(out, []byte(assetBaseMeta), []byte(`<meta name="culibox-asset-base" content="`+base+`">`), 1)
}

// serveIndex answers "/" with index.html rewritten for this build, revalidated on every load.
func (a *staticAssets) serveIndex(w http.ResponseWriter, r *http.Request) bool {
	a.indexOnce.Do(func() {
		raw, err := fs.ReadFile(a.fsys, "index.html")
		if err != nil {
			return
		}
		e := &a.indexEntry
		e.raw = rewriteIndex(raw, "/v/"+a.buildID())
		sum := sha256.Sum256(e.raw)
		e.tag, e.ctype, e.ok = hex.EncodeToString(sum[:])[:16], "text/html; charset=utf-8", true
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(e.raw)
		_ = zw.Close()
		if buf.Len() < len(e.raw) {
			e.gz = buf.Bytes()
		}
	})
	e := &a.indexEntry
	if !e.ok {
		return false
	}
	a.write(w, r, "index.html", e.raw, e.gz, e.tag, e.ctype, "no-cache")
	return true
}

// serveVersioned answers /v/<id>/<rest>, given "<id>/<rest>": only a regular file under vendor/ or xterm/, by a clean
// path, else 404. The current build's id gets a one-year immutable cache. Any other id is a page that was opened before
// the server was upgraded: it gets the current file revalidated (no-cache), never a long max-age, so an old URL can never
// pin a file across builds.
func (a *staticAssets) serveVersioned(w http.ResponseWriter, r *http.Request, idAndRest string) {
	id, rest, ok := strings.Cut(idAndRest, "/")
	if !ok || rest != path.Clean(rest) || !(strings.HasPrefix(rest, "vendor/") || strings.HasPrefix(rest, "xterm/")) {
		http.NotFound(w, r)
		return
	}
	cc := "no-cache"
	if id == a.buildID() {
		cc = staticImmutable
	}
	if !a.serve(w, r, rest, cc) {
		http.NotFound(w, r)
	}
}
