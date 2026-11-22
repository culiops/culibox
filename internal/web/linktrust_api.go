package web

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/datacompat"
	"github.com/culiops/culibox/internal/linktrust"
)

// LinkTrust is the per-person list of hosts whose agent links open without the
// "Open this link?" dialog, with the audited add/remove the dialog and
// Settings › Trusted hosts drive. *linktrust.Service satisfies it.
type LinkTrust interface {
	List(actor string) ([]linktrust.Host, error)
	Add(host string, subdomains bool, actor string) (linktrust.Host, error)
	Remove(host, actor string) error
}

var _ LinkTrust = (*linktrust.Service)(nil)

const (
	linkTrustBrowserOnlyMsg = "changing trusted link hosts needs this page's signed-in browser session"
	linkTrustFailedMsg      = "Could not change the trusted hosts. The full error is in the CuliBox log."
	linkTrustRefusedMsg     = "That host cannot be trusted. Only a plain host name such as docs.example.com can: no address, port, path or punycode name."
	linkTrustCapMsg         = "Too many trusted hosts. Remove one first."
	linkTrustNotFoundMsg    = "That host is not in your trusted hosts."
	linkTrustCorruptMsg     = "The trusted hosts list is damaged, so it cannot be changed. Fix or delete link-trust.json in CuliBox's data folder."
	linkTrustNewerMsg       = "link-trust.json was written by a newer CuliBox. Update CuliBox to change trusted hosts."
	linkTrustKind           = "trusted_link_host"
	linkTrustSubdomains     = "and its subdomains"
)

// LinkTrustAudit adapts a credential-audit recorder to the AuditFunc the
// link-trust service calls before every save: one row per trusted, changed or
// removed host, the host as the name and "and its subdomains" as the detail
// when the row covers them.
func LinkTrustAudit(rec CredRecorder) linktrust.AuditFunc {
	return func(action, host string, subdomains bool, actor string) error {
		e := broker.CredEvent{Action: action, Kind: linkTrustKind, Name: host, Actor: actor}
		if subdomains {
			e.Detail = linkTrustSubdomains
		}
		return rec.RecordCred(e)
	}
}

// linkTrustRow is one entry of /api/link-trust. The actor stays server-side:
// the page only ever sees its own rows.
type linkTrustRow struct {
	Host       string    `json:"host"`
	Subdomains bool      `json:"subdomains"`
	Added      time.Time `json:"added"`
}

// linkTrustView is the reply of GET (and of a successful POST or DELETE): the
// caller's hosts, oldest first. A list that cannot be read is empty with
// corrupt set, and the page keeps every link asking. newer is set (with
// corrupt) when the file was written by a newer CuliBox, so the page says
// "update CuliBox" rather than "damaged".
func (s *Server) linkTrustView() map[string]any {
	list, err := s.LinkTrust.List(storeActor)
	if err != nil {
		// The page only learns "corrupt"; the cause (damaged or newer) is for the log.
		log.Printf("link trust: list: %v", err)
	}
	rows := make([]linkTrustRow, 0, len(list))
	for _, h := range list {
		rows = append(rows, linkTrustRow{Host: h.Host, Subdomains: h.Subdomains, Added: h.Added})
	}
	return map[string]any{"hosts": rows, "corrupt": err != nil, "newer": errors.Is(err, datacompat.ErrNewer)}
}

// writeLinkTrustErr maps a link-trust error to the coded reply the page acts
// on. The text is fixed: a raw error can name the data folder or the audit
// store, so it goes to the log only.
func writeLinkTrustErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, linktrust.ErrRefused):
		log.Printf("link trust: %v", err)
		writeErrCode(w, http.StatusBadRequest, linkTrustRefusedMsg, "refused")
	case errors.Is(err, linktrust.ErrCap):
		writeErrCode(w, http.StatusConflict, linkTrustCapMsg, "cap")
	case errors.Is(err, datacompat.ErrNewer):
		// Still list_corrupt for the page, but never "delete it": the file holds a
		// newer CuliBox's data.
		log.Printf("link trust: %v", err)
		writeErrCode(w, http.StatusConflict, linkTrustNewerMsg, "list_corrupt")
	case errors.Is(err, linktrust.ErrCorrupt):
		log.Printf("link trust: %v", err)
		writeErrCode(w, http.StatusConflict, linkTrustCorruptMsg, "list_corrupt")
	case errors.Is(err, linktrust.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, linkTrustNotFoundMsg, "not_found")
	default:
		log.Printf("link trust: %v", err)
		writeErrCode(w, http.StatusInternalServerError, linkTrustFailedMsg, "failed")
	}
}

// handleLinkTrust serves /api/link-trust (GET list, POST add, DELETE remove).
// Writes are browser-only: a token holder, and so any agent, must not make its
// own links skip the confirm dialog.
func (s *Server) handleLinkTrust(w http.ResponseWriter, r *http.Request) {
	if s.LinkTrust == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && !browserOnly(w, r, linkTrustBrowserOnlyMsg) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.linkTrustView())
	case http.MethodPost:
		var req struct {
			Host       string `json:"host"`
			Subdomains bool   `json:"subdomains"`
		}
		if !decodeVMBody(w, r, &req) {
			return
		}
		if _, err := s.LinkTrust.Add(req.Host, req.Subdomains, storeActor); err != nil {
			writeLinkTrustErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.linkTrustView())
	case http.MethodDelete:
		var req struct {
			Host string `json:"host"`
		}
		if !decodeVMBody(w, r, &req) {
			return
		}
		if err := s.LinkTrust.Remove(req.Host, storeActor); err != nil {
			writeLinkTrustErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.linkTrustView())
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
