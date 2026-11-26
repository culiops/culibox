package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/secretchain"
	"github.com/culiops/culibox/internal/store"
)

// handleProfiles serves GET (list views) and POST (create a runtime profile).
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Profiles.Views())
	case http.MethodPost:
		doc, err := decodeProfile(w, r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// A page sends the schema it loaded with; one from before an update would
		// create a profile without the fields it does not know. A bare POST (an
		// API client) sends none and is accepted.
		if schemaStale(doc.Schema) {
			writePageOutdated(w)
			return
		}
		p := doc.AgentConfig
		rev, err := s.Profiles.CreateWith(p, SaveOpts{AckInPlaceHistory: doc.AckInPlaceHistory, AckMergeHome: doc.AckMergeHome})
		if err != nil {
			writeSaveErr(w, err)
			return
		}
		created := struct {
			Name     string   `json:"name"`
			Rev      string   `json:"rev"`
			Warnings []string `json:"warnings,omitempty"`
		}{p.Name, rev, saveWarnings(s, p)}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleProfile serves GET, PUT (update) and DELETE on /api/profiles/{name}.
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
		rev, schema := profileRev(p), profileSchema()
		writeJSON(w, http.StatusOK, profileDoc{AgentConfig: p, Rev: &rev, Schema: &schema})
	case http.MethodPut:
		doc, err := decodeProfile(w, r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// The schema first: a page from before an update must be told to reload,
		// not that the profile changed (Load latest would drop the new fields
		// again). A missing schema is refused like a missing rev, so a page from
		// before the schema existed is too.
		if doc.Schema == nil || *doc.Schema == "" {
			writeErrCode(w, http.StatusPreconditionRequired, schemaRequiredMsg, "rev_required")
			return
		}
		if schemaStale(doc.Schema) {
			writePageOutdated(w)
			return
		}
		if doc.Rev == nil || *doc.Rev == "" {
			writeErrCode(w, http.StatusPreconditionRequired, revRequiredMsg, "rev_required")
			return
		}
		p := doc.AgentConfig
		p.Name = name // the path is authoritative for the name
		rev, err := s.Profiles.UpdateWith(p, *doc.Rev, SaveOpts{AckInPlaceHistory: doc.AckInPlaceHistory, AckMergeHome: doc.AckMergeHome})
		if err != nil {
			writeSaveErr(w, err)
			return
		}
		// The revision of what was stored goes back, so a client whose re-read
		// fails still saves against the right one next time.
		saved := struct {
			Rev      string   `json:"rev"`
			Warnings []string `json:"warnings,omitempty"`
		}{rev, saveWarnings(s, p)}
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		// The revision the page opened the profile at, in the query (a DELETE has
		// no body to rely on). A delete without one is refused like a PUT is: a
		// page from before it existed must not delete what was edited since.
		if err := s.Profiles.Remove(name, r.URL.Query().Get("rev")); err != nil {
			switch {
			case errors.Is(err, store.ErrNotFound):
				writeErr(w, http.StatusNotFound, "no such editable profile")
			case errors.Is(err, errDeleteRevRequired):
				writeErrCode(w, http.StatusPreconditionRequired, deleteRevRequiredMsg, "rev_required")
			case errors.Is(err, ErrProfileChanged):
				writeErrCode(w, http.StatusConflict, deleteChangedMsg, "profile_changed")
			case errors.Is(err, store.ErrChangedOnDisk):
				writeErrCode(w, http.StatusConflict, profilesFileChangedMsg, "profiles_file_changed")
			case errors.Is(err, store.ErrUnreadable):
				writeErrCode(w, http.StatusConflict, profilesFileUnreadableMsg, "profiles_file_unreadable")
			default:
				var sf *storeFailure
				if errors.As(err, &sf) {
					err = sf.cause
				}
				log.Printf("profile delete: %v", err)
				writeErrCode(w, http.StatusInternalServerError, deleteFailedMsg, "delete_failed")
			}
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
	if w := p.EgressWarning(); w != "" {
		warnings = append(warnings, w)
	}
	if s.SecretCheck != nil {
		for _, name := range p.Secrets {
			pres, err := s.SecretCheck(name)
			if err != nil {
				log.Printf("profile %q: checking secret %q: %v", p.Name, name, err)
				continue
			}
			if pres == secretchain.Absent {
				if s.SecretStoredOutside != nil && s.SecretStoredOutside(name) {
					warnings = append(warnings, secretStoredOutsideWarning(name, p.Runtime == config.RuntimeBox))
				} else {
					warnings = append(warnings, secretUnresolvedWarning(name))
				}
			}
		}
	}
	for _, w := range warnings {
		log.Printf("profile %q: %s", p.Name, w)
	}
	return warnings
}

// secretUnresolvedWarning is the Save warning for a secret name that no
// configured source resolves. The save stands: the source may be filled in later.
func secretUnresolvedWarning(name string) string {
	return fmt.Sprintf("secret %q is not found in any configured secret source; a session with this profile will refuse to start", name)
}

// secretStoredOutsideWarning is the Save warning for a secret name the store
// holds while no store secret_source is configured, so nothing reads it (#336).
// A service token binds only a Box profile (config validateTokens).
func secretStoredOutsideWarning(name string, boxed bool) string {
	token := "or give the profile a service token instead"
	if !boxed {
		token = "or set the profile to run in a Box and give it a service token instead"
	}
	return fmt.Sprintf(`secret %q is a Stored value, but the credential store is not one of the configured secret_sources, so a session with this profile will refuse to start. Add {"type": "store"} to secret_sources in config.json and restart CuliBox, %s`, name, token)
}

// decodeProfile reads a profile with its rev and schema from the request body
// (64 KiB at most). POST ignores the rev and checks the schema only if sent; PUT
// requires both.
func decodeProfile(w http.ResponseWriter, r *http.Request) (profileDoc, error) {
	var doc profileDoc
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&doc); err != nil {
		return profileDoc{}, errors.New("invalid profile json")
	}
	return doc, nil
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
	// Saved against the revision just read, so an edit landing between that
	// read and this write is refused (409), not overwritten.
	rev := profileRev(fresh)
	fresh.AWS = nil
	fresh.AWSConnections = []string{created}
	if _, err := s.Profiles.Update(fresh, rev); err != nil {
		s.removeConverted(name, created)
		writeSaveErr(w, err)
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
