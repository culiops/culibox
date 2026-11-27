package web

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxread"
)

// KeptService is the box manager's kept-folder stores (box.Manager), as the
// profile view reads them and the user forgets one. Everything in a store was
// written by a box: what it returns is data, and a path into a store is only
// ever opened through it.
type KeptService interface {
	KeptStores(profile string) []box.KeptStoreView
	KeptStore(profile, key string) (box.KeptStoreView, bool)
	KeptOpenRoot(key, rel string) (*os.Root, error)
	// KeptForget's actor is who asked, as spawnActor names them.
	KeptForget(ctx context.Context, key, rel, actor string) error
	// KeptForgetClashes removes only the older copies of rel, as actor asked.
	KeptForgetClashes(ctx context.Context, key, rel, actor string) error
}

const (
	// keptListMax bounds one listing of a kept folder's directory. A box can
	// write any number of names into a folder its boxes mount read-write.
	keptListMax = 5000
	// keptForgetTimeout bounds a forget: it waits for a keep pass of the store
	// (60 s at most) and for opMu.
	keptForgetTimeout = 2 * time.Minute
	keptForgetBodyMax = 4 << 10

	msgKeptSessions   = "end this profile's boxed sessions first"
	msgKeptStopping   = "a box of this profile is still stopping; try again in a minute"
	msgKeptRunUnknown = "could not tell whether this profile's boxes are running; try again"
	msgKeptBusy       = "the kept folders are busy; try again in a minute"
	msgKeptNotRemoved = "the folder is forgotten and no box gets it again, but some of its files could not be removed (see the CuliBox log)"
	msgKeptForgetFail = "could not forget the folder"
	msgKeptClashFail  = "some of the older copies could not be removed (see the CuliBox log)"
)

// keptRoute splits a /api/profiles/ remainder of the form
// <profile>/kept[/list|/file|/forget] into the profile and the suffix after
// "/kept". A profile name has no "/", so it cannot be mistaken for one.
func keptRoute(p string) (profile, sub string, ok bool) {
	profile, rest, found := strings.Cut(p, "/")
	if !found || profile == "" {
		return "", "", false
	}
	switch rest {
	case "kept", "kept/list", "kept/file", "kept/forget":
		return profile, strings.TrimPrefix(rest, "kept"), true
	}
	return "", "", false
}

// handleKept serves the kept folders of profile:
//
//	GET  /api/profiles/{name}/kept                         {stores: [KeptStoreView]}, from state.json only
//	GET  /api/profiles/{name}/kept/list?key=&path=         one directory, no link followed
//	GET  /api/profiles/{name}/kept/file?key=&path=[&download=1]  one regular file, read-only
//	POST /api/profiles/{name}/kept/forget {key, path}      behind csrfGuard, audited box_kept_forget as spawnActor's
//	POST /api/profiles/{name}/kept/forget {key, path, clash: true}  only the older copies of path go
//
// path is <rel>[/<inner>] or clash/<box>/<rel>[/<inner>] (box.KeptSplitPath).
// A key must be one of the profile's stores. Every refusal of a name or a
// path (unknown store, a link, a FIFO, a device, anything that is not there)
// is the same quiet 404, as /boxfile does: the answer does not tell a box
// which of its files exist or why one was refused.
func (s *Server) handleKept(w http.ResponseWriter, r *http.Request, profile, sub string) {
	method := http.MethodGet
	if sub == "/forget" {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.Profiles.Get(profile); !ok {
		writeErr(w, http.StatusNotFound, "no such profile")
		return
	}
	switch sub {
	case "":
		stores := []box.KeptStoreView{}
		if s.Kept != nil {
			stores = append(stores, s.Kept.KeptStores(profile)...)
		}
		writeJSON(w, http.StatusOK, map[string]any{"stores": stores})
	case "/list":
		s.handleKeptList(w, r, profile)
	case "/file":
		s.handleKeptFile(w, r, profile)
	case "/forget":
		s.handleKeptForget(w, r, profile)
	}
}

// keptOpen opens the folder that p names in store key of profile, and returns
// it with the path below it.
func (s *Server) keptOpen(profile, key, p string) (*os.Root, string, bool) {
	if s.Kept == nil {
		return nil, "", false
	}
	if _, ok := s.Kept.KeptStore(profile, key); !ok {
		return nil, "", false
	}
	folder, inner, ok := box.KeptSplitPath(p)
	if !ok {
		return nil, "", false
	}
	root, err := s.Kept.KeptOpenRoot(key, folder)
	if err != nil {
		return nil, "", false
	}
	return root, inner, true
}

func (s *Server) handleKeptList(w http.ResponseWriter, r *http.Request, profile string) {
	q := r.URL.Query()
	p := q.Get("path")
	root, inner, ok := s.keptOpen(profile, q.Get("key"), p)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer root.Close()
	ents, more, ok := boxread.ReadDirIn(root, inner, keptListMax)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	out := make([]fsEntry, 0, len(ents))
	for _, e := range ents {
		ent := fsEntry{Name: e.Name, IsDir: e.Mode.IsDir(), Link: e.Mode&fs.ModeSymlink != 0}
		if e.Mode.IsRegular() {
			ent.Size = e.Size
		}
		out = append(out, ent)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	resp := map[string]any{"path": p, "entries": out, "readonly": true}
	if more {
		resp["truncated"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleKeptFile(w http.ResponseWriter, r *http.Request, profile string) {
	q := r.URL.Query()
	root, inner, ok := s.keptOpen(profile, q.Get("key"), q.Get("path"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer root.Close()
	f, fi, ok := boxread.OpenIn(root, inner)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	serveFsFile(w, r, f, fi, keptHeaderName(path.Base(inner)), true)
}

// keptHeaderName is a box-chosen file name made safe for a response header
// (the download's Content-Disposition): every control character, quote,
// backslash and invalid byte becomes "_". The extension, which picks how the
// file is shown, stays.
func keptHeaderName(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r == utf8.RuneError {
			if _, n := utf8.DecodeRuneInString(name[i:]); n <= 1 {
				b.WriteByte('_')
				continue
			}
		}
		if unicode.IsControl(r) || r == '"' || r == '\\' || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Server) handleKeptForget(w http.ResponseWriter, r *http.Request, profile string) {
	var req struct {
		Key   string `json:"key"`
		Path  string `json:"path"`
		Clash bool   `json:"clash"` // only the older copies (clash/) of path
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, keptForgetBodyMax))
	if err != nil || !decodeBytesStrict(raw, &req) {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if s.Kept == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, ok := s.Kept.KeptStore(profile, req.Key); !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), keptForgetTimeout)
	defer cancel()
	if req.Clash {
		s.handleKeptForgetClashes(ctx, w, profile, req.Key, req.Path, spawnActor(r))
		return
	}
	err = s.Kept.KeptForget(ctx, req.Key, req.Path, spawnActor(r))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"forgotten": req.Path})
	case errors.Is(err, box.ErrKeptStopping):
		writeErr(w, http.StatusConflict, msgKeptStopping)
	case errors.Is(err, box.ErrKeptInUse): // ErrKeptSessions, or any other use
		writeErr(w, http.StatusConflict, msgKeptSessions)
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, box.ErrKeptRunUnknown):
		writeErr(w, http.StatusServiceUnavailable, msgKeptRunUnknown)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		writeErr(w, http.StatusServiceUnavailable, msgKeptBusy)
	case errors.Is(err, box.ErrKeptNotRemoved):
		writeErr(w, http.StatusInternalServerError, msgKeptNotRemoved)
	default:
		log.Printf("profile %q: forgetting a kept folder: %v", profile, err)
		writeErr(w, http.StatusInternalServerError, msgKeptForgetFail)
	}
}

// handleKeptForgetClashes is a forget of the older copies of path alone: the
// kept folder stays, so no box is in the way.
func (s *Server) handleKeptForgetClashes(ctx context.Context, w http.ResponseWriter, profile, key, p, actor string) {
	err := s.Kept.KeptForgetClashes(ctx, key, p, actor)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"forgotten": p, "clash": true})
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		writeErr(w, http.StatusServiceUnavailable, msgKeptBusy)
	case errors.Is(err, box.ErrKeptClashNotRemoved):
		writeErr(w, http.StatusInternalServerError, msgKeptClashFail)
	default:
		log.Printf("profile %q: removing the older copies of a kept folder: %v", profile, err)
		writeErr(w, http.StatusInternalServerError, msgKeptForgetFail)
	}
}
