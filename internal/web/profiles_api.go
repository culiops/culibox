package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/store"
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
		}{p.Name, saveWarnings(s, p)}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleProfile serves PUT (update) and DELETE on /api/profiles/{name}.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	if base, ok := strings.CutSuffix(name, convertAWSSuffix); ok && base != "" {
		s.handleConvertAWS(w, r, base)
		return
	}
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
		if warnings := saveWarnings(s, p); len(warnings) > 0 {
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

// saveWarnings are the saved profile's secret-looking clone shares plus its
// context_paths warnings. Each is logged as well as returned: the share is the
// user's call, so the save stands.
func saveWarnings(s *Server, p config.AgentConfig) []string {
	warnings := append(config.ShareWarnings(p.CloneShare), s.Profiles.ContextWarnings(p)...)
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

// convertAWSSuffix is the path word after a profile name that converts its
// inline AWS binding. A profile name has no "/", so it cannot be mistaken for
// one.
const convertAWSSuffix = "/convert-aws"

// connNameMax is the longest AWS connection name.
const connNameMax = 63

// convertSlug turns a profile name into an AWS connection name: lowercase, any
// character outside [a-z0-9-] becomes "-", leading non-alphanumerics go, at
// most 63 characters; "aws" when nothing is left.
func convertSlug(profile string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(profile) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), "-")
	if len(out) > connNameMax {
		out = out[:connNameMax]
	}
	if out == "" {
		return "aws"
	}
	return out
}

// convertFallback is "<slug>-aws", the slug cut so the whole stays within 63.
func convertFallback(slug string) string {
	const suffix = "-aws"
	return slug[:min(len(slug), connNameMax-len(suffix))] + suffix
}

// connNameUnusable are names no connection may take: the config package's
// reserved names and the path words under /api/credentials/aws. Such a slug is
// treated like a taken one, so a profile named "default" still converts.
func connNameUnusable(name string) bool {
	return slices.Contains([]string{"default", "agentbox"}, name) || slices.Contains(awsRouteWords, name)
}

// convertTaken reports whether PutAWS refused a create because the name is
// already an entry, in config.json (ErrCredSeed) or in the web file.
func convertTaken(err error) bool {
	return errors.Is(err, ErrCredExists) || errors.Is(err, ErrCredSeed)
}

// handleConvertAWS is POST /api/profiles/{name}/convert-aws {ack_write?}: it
// turns the profile's inline AWS binding into a web AWS connection named after
// the profile (or "<profile>-aws" when that is taken) and points the profile at
// it. The profile's box key changes with its binding, so its next session gets
// a new box. Same gate as the credential mutations: it can use the host signer.
//
// The two writes are sequential (lock order is CredentialManager, then
// ProfileManager): the connection first, then the profile; a profile that
// cannot be saved takes the new connection back out.
func (s *Server) handleConvertAWS(w http.ResponseWriter, r *http.Request, name string) {
	if !credGate(w, r, true) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.Creds == nil || s.CredAWS == nil {
		writeNoBroker(w)
		return
	}
	var req struct {
		AckWrite bool `json:"ack_write"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, credBodyMax))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(strings.TrimSpace(string(raw))) > 0 && !decodeBytesStrict(raw, &req) {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	p, ok := s.Profiles.Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such profile")
		return
	}
	if p.AWS == nil {
		writeErr(w, http.StatusBadRequest, "this profile has no inline AWS binding to convert")
		return
	}
	// A private copy: the pointer in p may be shared with the stored profile, and
	// the comparison after the check must see the binding as it was read.
	orig := p.AWS.Clone()
	slug := convertSlug(name)
	conn := config.AWSConnection{Description: "converted from profile " + name, AWSBinding: *orig.Clone()}
	created := ""
	for _, candidate := range []string{slug, convertFallback(slug)} {
		if connNameUnusable(candidate) {
			continue
		}
		conn.Name = candidate
		if _, err := s.Creds.PutAWS(r.Context(), conn, true, req.AckWrite); err != nil {
			if convertTaken(err) {
				continue
			}
			writeCredErr(w, err)
			return
		}
		created = candidate
		break
	}
	if created == "" {
		writeErrCode(w, http.StatusConflict, "an AWS connection named "+slug+" or "+convertFallback(slug)+" is already defined", "exists")
		return
	}
	// The check above can take seconds. Read the profile again and build the
	// save from that copy: an edit made meanwhile survives, and a profile that
	// was deleted, converted by another request, or given a different inline
	// binding is left alone, with the connection just created taken back out.
	fresh, ok := s.Profiles.Get(name)
	if !ok || fresh.AWS == nil || !reflect.DeepEqual(fresh.AWS, orig) {
		s.removeConverted(name, created)
		writeErrCode(w, http.StatusConflict, "the profile changed while it was being converted: reload and try again", "profile_changed")
		return
	}
	fresh.AWS = nil
	fresh.AWSConnections = []string{created}
	if err := s.Profiles.Save(fresh); err != nil {
		s.removeConverted(name, created)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"connection": created})
}

// removeConverted takes back the connection a convert created but could not use.
// A delete that fails is logged (names only); the caller's answer stands.
func (s *Server) removeConverted(profile, conn string) {
	if err := s.Creds.Delete(credfile.KindAWS, conn); err != nil {
		log.Printf("convert-aws: profile %q not converted and connection %q could not be removed: %v", profile, conn, err)
	}
}
