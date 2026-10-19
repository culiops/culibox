package ghapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultBaseURL = "https://api.github.com"
	maxErrLen      = 300
	maxRespBytes   = 1 << 20
)

// ErrNotInstalled means the App is not installed on the owner (or cannot see
// the repository).
var ErrNotInstalled = errors.New("the GitHub App is not installed on this owner")

// ScopeError is the API's 422: the requested repositories or permissions are
// not something the installation can grant.
type ScopeError struct{ Msg string }

func (e *ScopeError) Error() string { return "token scope refused: " + e.Msg }

// APIError is any other non-2xx answer, or a transport failure (Status 0).
// Msg is the API's own message, capped; it never carries a token or JWT.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return "api request failed: " + e.Msg
	}
	return fmt.Sprintf("api status %d: %s", e.Status, e.Msg)
}

// Token is an installation access token and when the API says it expires.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// Client is a minimal REST client for the three calls the broker needs.
type Client struct {
	BaseURL string       // zero = https://api.github.com
	HTTP    *http.Client // nil = 10 s timeout, redirects never followed
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// do sends one request and returns the status and (bounded) body. Transport
// failures come back as *APIError{Status: 0}.
func (c Client) do(ctx context.Context, method, path, bearer string, body any) (int, []byte, error) {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return 0, nil, &APIError{Msg: "bad request"}
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "agent-box")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		// Report only the cause: url.Error would repeat the URL, which is fine,
		// but the message must never include request headers.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, &APIError{Msg: capMsg(err.Error())}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return 0, nil, &APIError{Msg: "reading response failed"}
	}
	return resp.StatusCode, data, nil
}

func capMsg(s string) string {
	if len(s) > maxErrLen {
		return s[:maxErrLen]
	}
	return s
}

// apiMessage extracts the API's "message" field, capped.
func apiMessage(data []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &m) != nil || m.Message == "" {
		return "no message"
	}
	return capMsg(m.Message)
}

// InstallationID finds the installation covering owner/repo (App JWT auth).
func (c Client) InstallationID(ctx context.Context, jwt, owner, repo string) (int64, error) {
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/installation"
	status, data, err := c.do(ctx, http.MethodGet, path, jwt, nil)
	if err != nil {
		return 0, err
	}
	switch {
	case status == http.StatusNotFound:
		return 0, ErrNotInstalled
	case status != http.StatusOK:
		return 0, &APIError{Status: status, Msg: apiMessage(data)}
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(data, &out) != nil || out.ID == 0 {
		return 0, &APIError{Status: status, Msg: "malformed installation response"}
	}
	return out.ID, nil
}

// CreateToken mints an installation token limited to repos and perms.
func (c Client) CreateToken(ctx context.Context, jwt string, id int64, repos []string, perms map[string]string) (Token, error) {
	body := struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}{repos, perms}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", id)
	status, data, err := c.do(ctx, http.MethodPost, path, jwt, body)
	if err != nil {
		return Token{}, err
	}
	switch status {
	case http.StatusCreated:
	case http.StatusUnprocessableEntity:
		return Token{}, &ScopeError{Msg: apiMessage(data)}
	default:
		return Token{}, &APIError{Status: status, Msg: apiMessage(data)}
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(data, &out) != nil || out.Token == "" {
		return Token{}, &APIError{Status: status, Msg: "malformed token response"}
	}
	return Token{Value: out.Token, ExpiresAt: out.ExpiresAt}, nil
}

// Revoke invalidates an installation token (authenticated by the token itself).
func (c Client) Revoke(ctx context.Context, token string) error {
	status, data, err := c.do(ctx, http.MethodDelete, "/installation/token", token, nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return &APIError{Status: status, Msg: apiMessage(data)}
	}
	return nil
}
