package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/culiops/culibox/internal/vmshares"
)

const vmFoldersBodyMax = 16 << 10

// vmFoldersBrowserOnly: every change to what the box VM sees comes from this
// page's own signed-in browser session, never a bearer token (a token holder
// must not widen the VM's view). Same rule as the store's lock.
func vmFoldersBrowserOnly(w http.ResponseWriter, r *http.Request) bool {
	if credKindOf(r) != credCookie || !strictSameOrigin(r) {
		writeErr(w, http.StatusForbidden, "changing the VM folders needs this page's signed-in browser session")
		return false
	}
	return true
}

func decodeVMBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, vmFoldersBodyMax))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.Decode(&json.RawMessage{}) != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// writeVMErr maps a folder-service error to the coded reply the page acts on.
func writeVMErr(w http.ResponseWriter, err error) {
	var ref *vmshares.Refused
	var con *vmshares.Conflict
	body := func(status int, code string) {
		writeJSON(w, status, map[string]string{"error": err.Error(), "code": code, "field": "path"})
	}
	switch {
	case errors.As(err, &ref):
		body(http.StatusBadRequest, "refused")
	case errors.As(err, &con):
		body(http.StatusBadRequest, "rw_in_ro")
	case errors.Is(err, ErrApplyRunning):
		writeErrCode(w, http.StatusConflict, err.Error(), "apply_running")
	case errors.Is(err, ErrSharesChanged):
		writeErrCode(w, http.StatusConflict, err.Error(), "shares_changed")
	case errors.Is(err, ErrListCorrupt):
		writeErrCode(w, http.StatusLocked, err.Error(), "list_corrupt")
	case errors.Is(err, ErrVMNotSetUp):
		writeErrCode(w, http.StatusBadRequest, err.Error(), "vm_not_set_up")
	case errors.Is(err, vmshares.ErrNotListed), errors.Is(err, ErrVMBadMode):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		// OS and colima text carries data-dir paths: the log has it, the page
		// gets fixed copy.
		log.Printf("box VM folders: %v", err)
		writeErr(w, http.StatusInternalServerError, vmFoldersFailed)
	}
}

const vmFoldersFailed = "Could not change the VM folders. The full error is in the CuliBox log."

// handleVMFolders serves /api/vm-folders (GET view, POST add, PATCH mode, DELETE remove).
func (s *Server) handleVMFolders(w http.ResponseWriter, r *http.Request) {
	if s.VMFolders == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && !vmFoldersBrowserOnly(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		v, err := s.VMFolders.View()
		if err != nil {
			log.Printf("box VM folders: reading the folder view: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not read the VM folders")
			return
		}
		writeJSON(w, http.StatusOK, v)
	case http.MethodPost:
		var req struct {
			Path   string `json:"path"`
			Mode   string `json:"mode"`
			DryRun bool   `json:"dry_run"`
		}
		if !decodeVMBody(w, r, &req) {
			return
		}
		v, res, err := s.VMFolders.Add(req.Path, req.Mode, req.DryRun)
		if err != nil {
			writeVMErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"view": v, "result": res})
	case http.MethodPatch:
		var req struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		}
		if !decodeVMBody(w, r, &req) {
			return
		}
		v, err := s.VMFolders.SetMode(req.Path, req.Mode)
		if err != nil {
			writeVMErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	case http.MethodDelete:
		v, err := s.VMFolders.Remove(r.URL.Query().Get("path"))
		if err != nil {
			writeVMErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleVMFoldersApply serves /api/vm-folders/apply (POST start, GET last job).
func (s *Server) handleVMFoldersApply(w http.ResponseWriter, r *http.Request) {
	if s.VMFolders == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		j, ok := s.VMFolders.ApplyStatus()
		if !ok {
			writeErr(w, http.StatusNotFound, "no apply has run")
			return
		}
		writeJSON(w, http.StatusOK, j)
	case http.MethodPost:
		if !vmFoldersBrowserOnly(w, r) {
			return
		}
		j, err := s.VMFolders.Apply()
		if err != nil {
			writeVMErr(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, j)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// vmDisplay shows p with a leading home directory as "~".
func vmDisplay(p, home string) string {
	if home == "" {
		return p
	}
	home = filepath.Clean(home)
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
