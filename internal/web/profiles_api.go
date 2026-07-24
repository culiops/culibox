package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/store"
)

// handleProfiles serves GET (list views) and POST (create a runtime profile).
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Profiles.Views())
	case http.MethodPost:
		p, err := decodeProfile(w, r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.Profiles.Save(p); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": p.Name})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleProfile serves PUT (update) and DELETE on /api/profiles/{name}.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	switch r.Method {
	case http.MethodPut:
		p, err := decodeProfile(w, r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		p.Name = name // the path is authoritative for the name
		if err := s.Profiles.Save(p); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.Profiles.Delete(name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "no such editable profile")
				return
			}
			writeErr(w, http.StatusInternalServerError, "delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func decodeProfile(w http.ResponseWriter, r *http.Request) (config.AgentConfig, error) {
	var p config.AgentConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
		return config.AgentConfig{}, errors.New("invalid profile json")
	}
	return p, nil
}
