// Options, the driver registry, and the bridge from configuration.
//
// The registry exists so that adding a connector is one directory plus one Register call
// rather than an edit to internal/fetch. FromConfig is the only place this package knows
// about internal/config, and the only place a source token is read: it is a secret named by
// sources[].auth.token_env, resolved at this boundary so it appears in no other signature.
//
// Domain and Limits stay as the raw maps the schema carries. Their keys differ per driver,
// so the driver decodes them with config.DecodeInto; this package must not learn what an
// org or a board is.
package source

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/maxwell-cudlitz/inget/internal/config"
)

// Options configures a connector. It mirrors one entry of the sources block, plus the one
// artifact limit a connector needs: a fragment larger than FragmentMaxBytes has to be split
// at the source, because splitting is the only way incrementality survives below file
// granularity and only the connector knows where a safe split point is.
type Options struct {
	Name             string // config source name
	Driver           string
	Token            string
	APIURL           string
	APIVersion       string
	Domain           map[string]any
	Limits           map[string]any
	FragmentMaxBytes int64
}

// opener builds a connector from options.
type opener func(ctx context.Context, opts Options) (Connector, error)

var (
	driversMu sync.RWMutex
	drivers   = map[string]opener{}
)

// Register makes a driver available to Open. It panics on a duplicate name, because two
// implementations answering to one configuration value is a build-time mistake and not a
// condition any run could recover from.
func Register(name string, open opener) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if _, exists := drivers[name]; exists {
		panic("source: driver " + name + " registered twice")
	}
	drivers[name] = open
}

// Open builds the connector described by opts.
func Open(ctx context.Context, opts Options) (Connector, error) {
	if opts.Driver == "" {
		return nil, fmt.Errorf("source %q has no driver", opts.Name)
	}
	driversMu.RLock()
	open, ok := drivers[opts.Driver]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown source driver %q (registered: %v)", opts.Driver, Registered())
	}
	conn, err := open(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("opening source %s: %w", opts.Name, err)
	}
	return conn, nil
}

// Registered lists the driver names, sorted so an error message is stable.
func Registered() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FromConfig converts one named source into options, resolving its token.
//
// A missing token is an error here rather than a silent fall through to an anonymous
// client: every supported API either refuses anonymous requests or throttles them to a rate
// that would make a run look hung rather than unauthorized.
func FromConfig(cfg *config.Config, name string) (Options, error) {
	src, ok := cfg.Source(name)
	if !ok {
		return Options{}, fmt.Errorf("no source named %q is configured", name)
	}
	token, err := cfg.Secret(src.Auth.TokenEnv)
	if err != nil {
		return Options{}, fmt.Errorf("source %s token: %w", name, err)
	}
	return Options{
		Name:             src.Name,
		Driver:           src.Driver,
		Token:            token,
		APIURL:           src.Auth.APIURL,
		APIVersion:       src.Auth.APIVersion,
		Domain:           src.Domain,
		Limits:           src.Limits,
		FragmentMaxBytes: cfg.Artifacts.BlobMaxBytes,
	}, nil
}
