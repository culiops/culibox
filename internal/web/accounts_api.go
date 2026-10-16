package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/agent-box/internal/account"
)

type accountView struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	TokenSecret     string `json:"token_secret,omitempty"`
	InheritPersonal bool   `json:"inherit_personal,omitempty"`
	Default         bool   `json:"default,omitempty"`
	LoginState      string `json:"login_state"` // token | logged_in | unknown
	ConfigDir       string `json:"config_dir"`  // ~-relative for display
}

func (s *Server) accountView(r account.Resolved) accountView {
	v := accountView{Name: r.Name, Provider: r.Provider, TokenSecret: r.TokenSecret,
		InheritPersonal: r.InheritPersonal, Default: r.IsDefault, LoginState: "unknown", ConfigDir: tildePath(s.Home, r.ConfigDir)}
	switch {
	case r.TokenSecret != "" && s.SecretOK != nil && s.SecretOK(r.TokenSecret):
		v.LoginState = "token"
	case fileExistsWeb(filepath.Join(r.ConfigDir, ".credentials.json")):
		v.LoginState = "logged_in"
	}
	return v
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list := s.Accounts.List()
		out := make([]accountView, 0, len(list))
		for _, a := range list {
			out = append(out, s.accountView(a))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var a account.Account
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields() // a config_dir in the request is an error, not ignored
		if err := dec.Decode(&a); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid account json")
			return
		}
		res, err := s.Accounts.Create(a)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, account.ErrExists) {
				code = http.StatusConflict
			}
			writeErr(w, code, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, s.accountView(res))
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/accounts/")
	switch r.Method {
	case http.MethodPut:
		var body struct {
			TokenSecret     string `json:"token_secret"`
			InheritPersonal bool   `json:"inherit_personal"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid account json")
			return
		}
		res, err := s.Accounts.Update(name, body.TokenSecret, body.InheritPersonal)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, account.ErrUnknown) {
				code = http.StatusNotFound
			}
			writeErr(w, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.accountView(res))
	case http.MethodDelete:
		if who := s.accountInUse(name); who != "" {
			writeErr(w, http.StatusConflict, "account "+name+" is used by "+who)
			return
		}
		root, err := s.Accounts.Delete(name)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, account.ErrUnknown) {
				code = http.StatusNotFound
			}
			writeErr(w, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"left_on_disk": tildePath(s.Home, root)})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// accountInUse names the first profile or live session that references name.
func (s *Server) accountInUse(name string) string {
	if s.Profiles != nil {
		for _, p := range s.Profiles.Views() {
			if p.Account == name {
				return "profile " + p.Name
			}
		}
	}
	if s.API != nil && s.API.Sessions != nil {
		for _, si := range s.API.Sessions.List() {
			if si.Account == name && !si.Exited {
				if si.Name != "" {
					return "session " + si.Name
				}
				return "session " + si.ID
			}
		}
	}
	return ""
}

func tildePath(home, p string) string {
	if home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p)
}

func fileExistsWeb(p string) bool { _, err := os.Stat(p); return err == nil }
