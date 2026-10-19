package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/store"
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
		created := struct {
			Name     string   `json:"name"`
			Warnings []string `json:"warnings,omitempty"`
		}{p.Name, shareWarnings(p)}
		writeJSON(w, http.StatusCreated, created)
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
	case http.MethodGet:
		p, ok := s.Profiles.Get(name)
		if !ok {
			writeErr(w, http.StatusNotFound, "no such profile")
			return
		}
		writeJSON(w, http.StatusOK, p)
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
		if warnings := shareWarnings(p); len(warnings) > 0 {
			writeJSON(w, http.StatusOK, map[string][]string{"warnings": warnings})
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

// handleOptions serves GET /api/options — the option definitions for the editor.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Profiles.OptionDefs())
}

// shareWarnings are the saved profile's secret-looking clone shares. Each is
// logged as well as returned: the share is the user's call, so the save stands.
func shareWarnings(p config.AgentConfig) []string {
	warnings := config.ShareWarnings(p.CloneShare)
	for _, w := range warnings {
		log.Printf("profile %q: %s", p.Name, w)
	}
	return warnings
}

func decodeProfile(w http.ResponseWriter, r *http.Request) (config.AgentConfig, error) {
	var p config.AgentConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
		return config.AgentConfig{}, errors.New("invalid profile json")
	}
	return p, nil
}
