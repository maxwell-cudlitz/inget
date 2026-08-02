// The HTTP layer: one client over net/http. No dependency on the gh CLI and no GitHub SDK —
// the connector uses five endpoints, and an SDK would be a larger surface than the code it
// replaced, for a smaller share of it exercised.
//
// Every request goes through do (request.go), so there is exactly one place that attaches
// credentials, waits on the limiter, feeds response headers back to it, and decides what is
// worth retrying.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/ratelimit"
)

// Defaults and bounds for the client. The API version is pinned so that a breaking change
// to the REST surface is a deliberate config edit rather than a surprise on a Tuesday.
const (
	DefaultAPIURL     = "https://api.github.com"
	DefaultAPIVersion = "2022-11-28"

	acceptJSON    = "application/vnd.github+json"
	acceptTarball = "application/vnd.github+json"

	// maxAttempts bounds retries per request. The limiter already waits out a throttle,
	// so a failure past this one is a persistent server fault, not congestion.
	maxAttempts = 4

	// requestTimeout bounds one JSON request. The tarball endpoint gets streamTimeout
	// instead, because it transfers an archive rather than a document.
	requestTimeout = 30 * time.Second
	streamTimeout  = 5 * time.Minute
)

// backoffBase is the delay before the second attempt, doubled per attempt after. It governs 5xx
// retries only; a throttle waits as long as the limiter says. It is a variable rather than a
// constant so the tests can exercise the retry path without sleeping through it.
var backoffBase = 500 * time.Millisecond

// client talks to one GitHub API host. It is safe for concurrent use.
type client struct {
	http    *http.Client
	base    *url.URL
	token   string
	version string
	limiter ratelimit.Limiter
}

// newClient builds a client for apiURL, defaulting the host and API version when config
// leaves them empty. The transport carries no timeout of its own: each attempt bounds
// itself through the context, so one endpoint's budget cannot govern another's.
func newClient(apiURL, token, version string, limiter ratelimit.Limiter) (*client, error) {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	base, err := url.Parse(strings.TrimSuffix(apiURL, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("github api_url %q: %w", apiURL, err)
	}
	if base.Host == "" {
		return nil, fmt.Errorf("github api_url %q has no host", apiURL)
	}
	if version == "" {
		version = DefaultAPIVersion
	}
	if limiter == nil {
		limiter = ratelimit.Unlimited{}
	}
	return &client{
		http:    &http.Client{},
		base:    base,
		token:   token,
		version: version,
		limiter: limiter,
	}, nil
}

// resolve turns an API-relative reference into an absolute URL. A reference that is already
// absolute — a Link header's next page — is used unchanged.
func (c *client) resolve(ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("github request %q: %w", ref, err)
	}
	if u.IsAbs() {
		return u.String(), nil
	}
	resolved := c.base.ResolveReference(&url.URL{
		Path:     strings.TrimPrefix(u.Path, "/"),
		RawQuery: u.RawQuery,
	})
	return resolved.String(), nil
}

// getJSON fetches a JSON document and decodes it into out.
func (c *client) getJSON(ctx context.Context, ref string, out any) error {
	resp, err := c.do(ctx, ref, acceptJSON, requestTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s: %w", ref, err)
	}
	return nil
}

// paginate walks a Link-header-paginated endpoint, handing each page's raw body to onPage.
// The body stays raw because the two shapes GitHub uses — a bare array, and a search result
// wrapping one — differ per endpoint, and the caller knows which it asked for.
//
// onPage returning an error stops the walk and the error is returned unchanged, so a caller
// can end enumeration with its own sentinel.
func (c *client) paginate(ctx context.Context, ref string, onPage func(body []byte) error) error {
	for ref != "" {
		resp, err := c.do(ctx, ref, acceptJSON, requestTimeout)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", ref, err)
		}
		if err := onPage(body); err != nil {
			return err
		}
		ref = nextLink(resp.Header.Get("Link"))
	}
	return nil
}

// stream opens a long-running download. The caller closes the body, which is also what
// releases the attempt's context.
func (c *client) stream(ctx context.Context, ref string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, ref, acceptTarball, streamTimeout)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
