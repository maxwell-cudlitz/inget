// Package github fetches repositories as github/repo items.
//
// One item is one repository; one fragment is one file, or one slice of an oversized file.
// The connector is built around one observation about the GitHub API: the recursive tree
// endpoint returns a blob SHA per path, which is an exact content hash, for one request and
// without transferring any content. That gives exact per-file change detection for free, so
// the expensive call — the repository tarball — is made once per changed repository and never
// per file.
//
//	github.go   the connector, its registration, and webhook event mapping
//	domain.go   the domain and limits blocks, and the enumeration filters
//	client.go   the HTTP client
//	request.go  retries, response classification, Link pagination
//	repos.go    enumeration and the repository resource
//	pages.go    page decoding for the two listing shapes
//	tree.go     the recursive tree endpoint
//	tarball.go  streamed, bounded, in-memory archive extraction
//	filter.go   noise filtering and tier classification
//	split.go    sub-file fragments for oversized files
//	secrets.go  secret detection over candidate content
//	fetch.go    tree plus tarball plus filters into one Result
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/maxwell-cudlitz/inget/internal/ratelimit"
	"github.com/maxwell-cudlitz/inget/internal/source"
)

// Driver is the sources[].driver value this connector answers to, and Datatype is the only
// datatype it produces.
const (
	Driver   = "github"
	Datatype = "github/repo"
)

// init registers the driver. A binary reaches this connector by importing the package for
// its side effect, which is what keeps internal/fetch from knowing the driver exists.
func init() {
	source.Register(Driver, func(_ context.Context, opts source.Options) (source.Connector, error) {
		return New(opts)
	})
}

// Connector implements source.Connector for GitHub repositories. It is safe for concurrent
// use: the client, the limiter and the secret scanner all are, and nothing else is mutable.
type Connector struct {
	name    string
	client  *client
	domain  domain
	limits  limits
	scanner *scanner

	// fragmentMaxBytes is artifacts.blob_max_bytes: a file above it is split into
	// independently fingerprinted sub-file fragments rather than dropped, so
	// incrementality survives below file granularity.
	fragmentMaxBytes int64
}

// New builds a connector from options.
func New(opts source.Options) (*Connector, error) {
	d, err := decodeDomain(opts.Domain)
	if err != nil {
		return nil, err
	}
	l, err := decodeLimits(opts.Limits)
	if err != nil {
		return nil, err
	}
	http, err := newClient(opts.APIURL, opts.Token, opts.APIVersion, ratelimit.NewGitHub(l.RequestsPerSecond, l.MaxConcurrent))
	if err != nil {
		return nil, err
	}
	sc, err := newScanner(l.scanSecrets())
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = Driver
	}
	return &Connector{
		name:             name,
		client:           http,
		domain:           d,
		limits:           l,
		scanner:          sc,
		fragmentMaxBytes: opts.FragmentMaxBytes,
	}, nil
}

// Name returns the configured source name.
func (c *Connector) Name() string { return c.name }

// Datatypes returns the datatypes this connector produces.
func (c *Connector) Datatypes() []string { return []string{Datatype} }

// Close releases the connector. Nothing here owns a durable resource; the method exists so
// that a connector which does can be swapped in without changing any caller.
func (c *Connector) Close() error { return nil }

// Concurrency reports how many items may be fetched at once, from the limits block. The
// number is the source's, not the pipeline's: it is the API's tolerance being described.
func (c *Connector) Concurrency() int { return c.limits.MaxConcurrent }

// checkDatatype rejects a datatype this connector does not produce, naming what it does.
func (c *Connector) checkDatatype(datatype string) error {
	if !slices.Contains(c.Datatypes(), datatype) {
		return fmt.Errorf("source %s does not produce datatype %q (produces: %v)", c.name, datatype, c.Datatypes())
	}
	return nil
}

// ItemIDsFromEvent implements source.EventMapper for GitHub webhook payloads.
//
// Every repository-scoped event carries repository.full_name, which is the item ID this
// connector uses, so one field covers push, release, and the rest. A payload without it is an
// error rather than an empty result: silently fetching nothing would look like success.
func (c *Connector) ItemIDsFromEvent(payload []byte) ([]string, error) {
	var event struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("decoding github webhook payload: %w", err)
	}
	ids := make([]string, 0, 1+len(event.Repositories))
	if event.Repository.FullName != "" {
		ids = append(ids, event.Repository.FullName)
	}
	// Installation events carry a list instead of a single repository.
	for _, r := range event.Repositories {
		if r.FullName != "" {
			ids = append(ids, r.FullName)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("github webhook payload names no repository (expected repository.full_name)")
	}
	return ids, nil
}
