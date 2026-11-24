package boximagecheck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boxregistry"
)

const (
	// maxBody caps every answer read here, as boxregistry caps the registry's.
	maxBody   = 4 << 20
	userAgent = "culibox-image-check"
)

// The only strings a cache holds as an error: a class, never a raw body or a URL.
const (
	errUnreachable = "registry unreachable"
	errNotFound    = "not published yet"
	errUnexpected  = "unexpected registry answer"
	errScan        = "scan unreadable"
)

// ErrClassNewer is the CheckErr of a report whose cache file came from a newer
// CuliBox: the check may have run, this build cannot read what it learned.
const ErrClassNewer = "newer check file"

// Checker runs the anonymous daily check. Client carries the caller's timeout and
// proxy settings and is used as given; a nil Client is http.DefaultClient, so a
// forgotten one cannot panic a background goroutine.
type Checker struct {
	Client    *http.Client
	Registry  string // https://ghcr.io
	GitHubAPI string // https://api.github.com
	Now       func() time.Time
}

// NewChecker is the production check: ghcr.io and the GitHub API, anonymous. The
// client's default transport honours HTTPS_PROXY, timeout bounds each request, and
// nothing it sends carries a credential.
func NewChecker(timeout time.Duration) Checker {
	return Checker{
		Client:    &http.Client{Timeout: timeout},
		Registry:  "https://ghcr.io",
		GitHubAPI: "https://api.github.com",
	}
}

func (k Checker) now() time.Time {
	if k.Now == nil {
		return time.Now()
	}
	return k.Now()
}

// Check asks the registry for the image published for this binary's Dockerfile and
// GitHub for the weekly scan's open issue, and returns what it learned. The two are
// independent: one failing leaves the other's answer in place. It never returns nil;
// a failure lands in RegistryErr or ScanErr as a short class.
func (k Checker) Check(ctx context.Context) *Cache {
	if k.Client == nil {
		k.Client = http.DefaultClient
	}
	c := &Cache{Version: cacheVersion, CheckedAt: k.now(), Hash: boximage.Hash()}
	idx, err := boxregistry.Resolve(ctx, k.Client, k.Registry, "culiops/culibox", boximage.Hash())
	if err != nil {
		c.RegistryErr = registryClass(err)
	} else {
		c.Registry = &idx
	}
	if s, err := k.fetchScan(ctx); err != nil {
		c.ScanErr = errScan
	} else {
		c.Scan = s
	}
	return c
}

// registryClass sorts a Resolve error into the three things a user can act on: the
// image is not published, the registry cannot be reached (no HTTP answer at all: a
// dial, timeout, TLS or proxy failure), or it answered with something else.
func registryClass(err error) string {
	var urlErr *url.Error
	var netErr net.Error
	switch {
	case errors.Is(err, boxregistry.ErrNotFound):
		return errNotFound
	case errors.As(err, &urlErr), errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		return errUnreachable
	}
	return errUnexpected
}
