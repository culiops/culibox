package ghapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func checkHeaders(t *testing.T, r *http.Request, bearer string) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+bearer {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
	if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Errorf("api version = %q", got)
	}
	if got := r.Header.Get("User-Agent"); got != "agent-box" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestClientCreateTokenBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		checkHeaders(t, r, "jwt-1")
		b, _ := io.ReadAll(r.Body)
		want := `{"repositories":["a","b"],"permissions":{"contents":"read"}}`
		if strings.TrimSpace(string(b)) != want {
			t.Errorf("body = %s", b)
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"token":"ghs_x","expires_at":"2026-10-19T12:00:00Z"}`)
	}))
	defer srv.Close()
	c := Client{BaseURL: srv.URL}
	tok, err := c.CreateToken(context.Background(), "jwt-1", 42, []string{"a", "b"}, map[string]string{"contents": "read"})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "ghs_x" || !tok.ExpiresAt.Equal(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("token = %+v", tok)
	}
}

func TestClientInstallationID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/o/r/installation" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		checkHeaders(t, r, "jwt-1")
		io.WriteString(w, `{"id":77}`)
	}))
	defer srv.Close()
	id, err := Client{BaseURL: srv.URL}.InstallationID(context.Background(), "jwt-1", "o", "r")
	if err != nil || id != 77 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestClientErrorMapping(t *testing.T) {
	long := strings.Repeat("x", 500)
	cases := []struct {
		status int
		body   string
	}{
		{404, `{"message":"Not Found"}`},
		{422, `{"message":"permissions not granted"}`},
		{500, `{"message":"` + long + `"}`},
	}
	var cur int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(cases[cur].status)
		io.WriteString(w, cases[cur].body)
	}))
	defer srv.Close()
	c := Client{BaseURL: srv.URL}
	ctx := context.Background()

	cur = 0
	if _, err := c.InstallationID(ctx, "j", "o", "r"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("404: %v", err)
	}
	cur = 1
	_, err := c.CreateToken(ctx, "j", 1, []string{"r"}, nil)
	var se *ScopeError
	if !errors.As(err, &se) || se.Msg != "permissions not granted" {
		t.Fatalf("422: %v", err)
	}
	cur = 2
	_, err = c.CreateToken(ctx, "j", 1, []string{"r"}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 500 {
		t.Fatalf("500: %v", err)
	}
	if len(ae.Msg) > 300 {
		t.Fatalf("message not capped: %d", len(ae.Msg))
	}
}

func TestClientRevoke(t *testing.T) {
	status := 204
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/installation/token" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		checkHeaders(t, r, "ghs_tok")
		w.WriteHeader(status)
		if status != 204 {
			io.WriteString(w, `{"message":"Bad credentials"}`)
		}
	}))
	defer srv.Close()
	c := Client{BaseURL: srv.URL}
	if err := c.Revoke(context.Background(), "ghs_tok"); err != nil {
		t.Fatal(err)
	}
	status = 401
	err := c.Revoke(context.Background(), "ghs_tok")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("401: %v", err)
	}
	if strings.Contains(err.Error(), "ghs_tok") {
		t.Fatal("error leaks token")
	}
}

func TestClientTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	c := Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 100 * time.Millisecond}}
	start := time.Now()
	_, err := c.InstallationID(context.Background(), "j", "o", "r")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 0 {
		t.Fatalf("want APIError status 0, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("took too long")
	}
}

func TestClientNoRedirectFollow(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	}))
	defer srv.Close()
	_, err := Client{BaseURL: srv.URL}.InstallationID(context.Background(), "j", "o", "r")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 302 {
		t.Fatalf("want APIError 302, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}
