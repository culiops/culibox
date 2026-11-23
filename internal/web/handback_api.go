package web

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/culiops/culibox/internal/handback"
)

// HandbackStore is what the hand-back endpoints need of handback.Store: the
// records still waiting for review, and marking one reviewed.
type HandbackStore interface {
	Pending() ([]handback.Record, error)
	Reviewed(id string, revision int64) error
}

// handleHandback serves the files a boxed session handed back that the user has
// not yet reviewed (#346):
//
//	GET  /api/handback                  -> 200 {"records": [...]}, pending only
//	POST /api/handback/{id}/reviewed    -> 204; 400 bad id or body; 404 unknown id;
//	                                       409 scanned again since the page read it
//
// The POST body is {"revision": N}, the revision of the record as the page
// listed it: a record that changed since may hold flags the user has not seen,
// so it is left as it is (409) and the page lists it again.
//
// One bad record file does not hide the others: the good records are listed and
// the fault is logged, never sent (it names paths on the host).
func (a *API) handleHandback(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/handback")
	if rest == "" {
		a.listHandback(w, r)
		return
	}
	// Whatever sits between the prefix and /reviewed is the id, slashes and
	// dots included: the store decides whether it is one (400 when not).
	id, ok := strings.CutSuffix(strings.TrimPrefix(rest, "/"), "/reviewed")
	if !ok || !strings.HasPrefix(rest, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	a.markReviewed(w, r, id)
}

func (a *API) listHandback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	records := []handback.Record{}
	if a.Handback != nil {
		got, err := a.Handback.Pending()
		if err != nil {
			log.Printf("handback: some records could not be read: %v", err)
		}
		if got != nil {
			records = got
		}
	}
	writeJSON(w, http.StatusOK, map[string][]handback.Record{"records": records})
}

func (a *API) markReviewed(w http.ResponseWriter, r *http.Request, id string) {
	if !checkMutationGuard(w, r) {
		return
	}
	if a.Handback == nil {
		writeErr(w, http.StatusNotFound, "no such record")
		return
	}
	var body struct {
		Revision *int64 `json:"revision"`
	}
	if !decodeOpBody(w, r, &body) {
		return
	}
	if body.Revision == nil || *body.Revision < 0 {
		writeErr(w, http.StatusBadRequest, "revision is required")
		return
	}
	switch err := a.Handback.Reviewed(id, *body.Revision); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, handback.ErrChanged):
		writeErr(w, http.StatusConflict, "record changed since it was read")
	case errors.Is(err, handback.ErrInvalidID):
		writeErr(w, http.StatusBadRequest, "invalid record id")
	case errors.Is(err, handback.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such record")
	default:
		log.Printf("handback: marking %q reviewed: %v", id, err)
		writeErr(w, http.StatusInternalServerError, "could not mark the record reviewed")
	}
}
