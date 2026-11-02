package web

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/credtemplates"
	"github.com/culiops/culibox/internal/secretstore"
)

// These routes are the first the web has that accept secret values. A value
// decodes as secretBytes, is never echoed (no response, error, log line or
// audit detail carries one: errors name entries and ids only), is stored with
// secretstore.Set and nowhere else, and is zeroed when the request ends.

const (
	credTokensPrefix = "/api/credentials/tokens"
	credAppsPrefix   = "/api/credentials/github-apps"
	storeValuesPath  = "/api/store/values/"
)

// tokenRouteWords are the path words under /api/credentials/tokens; an entry
// named like one could not be edited or deleted by name.
var tokenRouteWords = []string{"from-template"}

// zeroAll zeroes every buffer in vs.
func zeroAll(vs ...[]byte) {
	for _, v := range vs {
		zero(v)
	}
}

// valueCheck refuses a value that is empty (400) or over the store's limit
// (413) before anything is written, so a refused request leaves no entry and no
// orphan value behind.
func valueCheck(w http.ResponseWriter, v []byte) bool {
	switch {
	case len(v) == 0:
		writeErr(w, http.StatusBadRequest, "the value is empty")
	case len(v) > secretstore.MaxValueBytes:
		writeErrCode(w, http.StatusRequestEntityTooLarge, "the value is larger than 64 KiB", "too_large")
	default:
		return true
	}
	return false
}

// handleCredTemplates is GET /api/credentials/templates.
func (s *Server) handleCredTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !credGate(w, r, false) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": credtemplates.All()})
}

// handleCredTokens serves /api/credentials/tokens and everything under it.
func (s *Server) handleCredTokens(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, credTokensPrefix)
	var allow []string
	var h func()
	switch rest {
	case "":
		allow, h = []string{http.MethodPost}, func() { s.credTokenSave(w, r, "", true) }
	case "/from-template":
		allow, h = []string{http.MethodPost}, func() { s.credTokenFromTemplate(w, r) }
	default:
		name := strings.TrimPrefix(rest, "/")
		if !strings.HasPrefix(rest, "/") || strings.Contains(name, "/") || name == "" {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		allow, h = []string{http.MethodPut, http.MethodDelete}, func() {
			if !credNameRe.MatchString(name) {
				writeErr(w, http.StatusBadRequest, "invalid name")
				return
			}
			if r.Method == http.MethodPut {
				s.credTokenSave(w, r, name, false)
				return
			}
			if err := s.Creds.Delete(credfile.KindToken, name); err != nil {
				writeCredErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	if !slices.Contains(allow, r.Method) {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !credGate(w, r, true) {
		return
	}
	h()
}

// tokenSaveReq is the body of a token create or replace by hand (the
// "other"/advanced path). Value is a pointer so that "" (an error) and absent
// (keep the current reference, or use the entry's own file reference) differ.
type tokenSaveReq struct {
	Entry config.StaticToken `json:"entry"`
	Value *secretBytes       `json:"value"`
}

func (s *Server) credTokenSave(w http.ResponseWriter, r *http.Request, name string, create bool) {
	var req tokenSaveReq
	defer func() {
		if req.Value != nil {
			zero(*req.Value)
		}
	}()
	if !decodeCredJSONMax(w, r, &req, credValueBodyMax) {
		return
	}
	entry := req.Entry
	if !create {
		if entry.Name == "" {
			entry.Name = name
		}
		if entry.Name != name {
			writeErr(w, http.StatusBadRequest, "the name in the body must match the URL")
			return
		}
	}
	if slices.Contains(tokenRouteWords, entry.Name) {
		writeErr(w, http.StatusBadRequest, "that name is reserved")
		return
	}
	values := map[string][]byte{}
	if req.Value != nil {
		if !valueCheck(w, *req.Value) {
			return
		}
		values[entry.Name] = *req.Value
	}
	if err := s.Creds.PutTokens([]config.StaticToken{entry}, values, create); err != nil {
		writeCredErr(w, err)
		return
	}
	writeSaved(w, create, entry.Name)
}

func writeSaved(w http.ResponseWriter, create bool, names ...string) {
	code := http.StatusOK
	if create {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"names": names})
}

// fromTemplateReq is the body of a token created from a catalog template.
// Values has one entry per secret the template needs, in the template's order.
type fromTemplateReq struct {
	Template string            `json:"template"`
	Name     string            `json:"name"`
	Label    string            `json:"label"`
	Prod     bool              `json:"prod"`
	Fields   map[string]string `json:"fields"`
	Values   []secretBytes     `json:"values"`
}

func (s *Server) credTokenFromTemplate(w http.ResponseWriter, r *http.Request) {
	var req fromTemplateReq
	defer func() {
		for _, v := range req.Values {
			zero(v)
		}
	}()
	if !decodeCredJSONMax(w, r, &req, credValueBodyMax) {
		return
	}
	if slices.Contains(tokenRouteWords, req.Name) {
		writeErr(w, http.StatusBadRequest, "that name is reserved")
		return
	}
	entries, err := credtemplates.Build(req.Template, req.Name, req.Label, req.Prod, req.Fields)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Values) != len(entries) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("this template needs %d value(s), got %d", len(entries), len(req.Values)))
		return
	}
	values := map[string][]byte{}
	for i, e := range entries {
		if !valueCheck(w, req.Values[i]) {
			return
		}
		values[e.Name] = req.Values[i]
	}
	if err := s.Creds.PutTokens(entries, values, true); err != nil {
		writeCredErr(w, err)
		return
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	writeSaved(w, true, names...)
}

// handleCredGitHubApps serves /api/credentials/github-apps and {name} under it.
func (s *Server) handleCredGitHubApps(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, credAppsPrefix)
	name := strings.TrimPrefix(rest, "/")
	var allow []string
	switch {
	case rest == "":
		allow = []string{http.MethodPost}
	case !strings.HasPrefix(rest, "/") || strings.Contains(name, "/") || name == "":
		writeErr(w, http.StatusNotFound, "not found")
		return
	default:
		allow = []string{http.MethodPut, http.MethodDelete}
	}
	if !slices.Contains(allow, r.Method) {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !credGate(w, r, true) {
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.credAppSave(w, r, "", true)
	case http.MethodPut:
		s.credAppSave(w, r, name, false)
	default:
		if err := s.Creds.Delete(credfile.KindGitHubApp, name); err != nil {
			writeCredErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type appSaveReq struct {
	App             config.GitHubApp `json:"app"`
	PrivateKeyValue *secretBytes     `json:"private_key_value"`
}

func (s *Server) credAppSave(w http.ResponseWriter, r *http.Request, name string, create bool) {
	var req appSaveReq
	defer func() {
		if req.PrivateKeyValue != nil {
			zero(*req.PrivateKeyValue)
		}
	}()
	if !decodeCredJSONMax(w, r, &req, credValueBodyMax) {
		return
	}
	app := req.App
	if !create {
		if app.Name == "" {
			app.Name = name
		}
		if app.Name != name {
			writeErr(w, http.StatusBadRequest, "the name in the body must match the URL")
			return
		}
	}
	var key []byte
	if req.PrivateKeyValue != nil {
		if !valueCheck(w, *req.PrivateKeyValue) {
			return
		}
		key = *req.PrivateKeyValue
	}
	if err := s.Creds.PutGitHubApp(app, key, create); err != nil {
		writeCredErr(w, err)
		return
	}
	writeSaved(w, create, app.Name)
}

// handleStoreValue is PUT and DELETE /api/store/values/{id}: set, replace or
// delete one stored value. Set and Remove work while the store is locked.
func (s *Server) handleStoreValue(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, storeValuesPath)
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "PUT, DELETE")
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !storeBrowserOnly(w, r, "changing values in") {
		return
	}
	if !secretstore.ValidID(id) {
		writeErr(w, http.StatusBadRequest, secretstore.ErrBadID.Error())
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.Creds.RemoveValue(id); err != nil {
			writeCredErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req struct {
		Value *secretBytes `json:"value"`
	}
	defer func() {
		if req.Value != nil {
			zero(*req.Value)
		}
	}()
	if !decodeCredJSONMax(w, r, &req, credValueBodyMax) {
		return
	}
	if req.Value == nil {
		writeErr(w, http.StatusBadRequest, "the value is empty")
		return
	}
	if !valueCheck(w, *req.Value) {
		return
	}
	if err := s.Creds.SetValue(id, *req.Value); err != nil {
		writeCredErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleStoreInit is POST /api/store/init {"passphrase"?}: create the encrypted
// store from the web. Once a store exists the launcher's SeccompGate refuses
// every boxed spawn on a host where Docker's seccomp profile is off, so init
// runs the same probe first and refuses there. On Linux the store is left
// locked by Init, so the passphrase just typed unlocks it with the default TTL.
func (s *Server) handleStoreInit(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !storeBrowserOnly(w, r, "creating") {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, storeBodyMax))
	defer zero(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	var req struct {
		Passphrase secretBytes `json:"passphrase"`
	}
	defer func() { zero(req.Passphrase) }()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if !decodeBytesStrict(raw, &req) {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
	}

	st := s.Store.Status()
	if st.Initialized {
		writeErrCode(w, http.StatusConflict, "the store already exists", "exists")
		return
	}
	if s.SeccompProbe != nil {
		on, err := s.SeccompProbe()
		switch {
		case err != nil:
			log.Printf("store init: seccomp probe: %v", err)
			writeErrCode(w, http.StatusConflict, "Could not check whether Docker seccomp is on, so the store was not created. Try again in a moment.", "seccomp_unknown")
			return
		case !on:
			writeErrCode(w, http.StatusConflict, "Docker seccomp is off on this host. Turn it on before creating the store, or every boxed session will be refused.", "seccomp_off")
			return
		}
	}
	if st.PassphraseMode && utf8.RuneCount(req.Passphrase) < secretstore.MinPassphraseChars {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("the passphrase must be at least %d characters", secretstore.MinPassphraseChars))
		return
	}
	switch err := s.Store.Init(req.Passphrase, storeActor); {
	case err == nil:
	case errors.Is(err, secretstore.ErrExists):
		writeErrCode(w, http.StatusConflict, "the store already exists", "exists")
		return
	default:
		// The error text is not shown: it can name a path.
		log.Printf("store init: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not create the store")
		return
	}
	unlocked := !st.PassphraseMode
	if st.PassphraseMode {
		if err := s.Store.Unlock(req.Passphrase, s.StoreUnlockTTL, storeActor); err != nil {
			log.Printf("store init: unlock after init: %v", err)
		} else {
			unlocked = true
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"initialized": true, "unlocked": unlocked})
}
