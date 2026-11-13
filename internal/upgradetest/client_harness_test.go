//go:build upgrade

package upgradetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Client talks to one serve over loopback. The cookie jar is shared by every
// copy, so a session cookie set by vN-1 is offered to vN on the same port.
type Client struct {
	Base   string
	HTTP   *http.Client
	Bearer string
}

// NewClient returns a cookie client for http://127.0.0.1:<port>. It never
// follows redirects: POST /login answers 303 and the cookie is all we need.
func NewClient(port string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		Base: "http://127.0.0.1:" + port,
		HTTP: &http.Client{
			Jar:           jar,
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Login posts the login token as the password form field.
func (c *Client) Login(token string) error {
	req, err := http.NewRequest("POST", c.Base+"/login", strings.NewReader(url.Values{"password": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.Base)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		return fmt.Errorf("login: status %d", resp.StatusCode)
	}
	return nil
}

// Do sends body as JSON (nil = no body) with Origin set to Base, the strict
// same-origin check the store and audit endpoints need. A 2xx JSON answer is
// decoded into out when out is non-nil; any other answer is returned as an
// error that carries the body.
func (c *Client) Do(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Origin", c.Base)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.Bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode: %v: %s", method, path, err, raw)
		}
	}
	return resp.StatusCode, nil
}

// WithBearer returns a client that authenticates with an API token only: no
// cookie jar, so the bearer is what the server sees.
func (c *Client) WithBearer(tok string) *Client {
	return &Client{
		Base:   c.Base,
		HTTP:   &http.Client{Timeout: c.HTTP.Timeout, CheckRedirect: c.HTTP.CheckRedirect},
		Bearer: tok,
	}
}
