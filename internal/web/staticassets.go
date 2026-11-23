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
