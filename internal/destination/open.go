// Options, the driver registry, and the bridge from configuration.
//
// The registry exists so that adding a sink is one file plus one Register call rather than
// an edit to the pipeline. FromConfig is the only place this package knows about
// internal/config, and the only place a destination DSN is read: it is a secret named by
// destinations[].dsn_env, and resolving it at the boundary keeps it out of every other
// signature here.
package destination

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/maxwell-cudlitz/inget/internal/config"
)

// Driver names.
const DriverPGVector = "pgvector"

// Storage types, matching the destinations[].storage vocabulary in config.
const (
	StorageHalfvec = "halfvec"
	StorageVector  = "vector"
)

// DefaultBatchSize is used when configuration names none. It is small enough that one
// failed batch is cheap to retry and large enough that per-statement overhead disappears.
const DefaultBatchSize = 500

// Options configures a destination. It mirrors one entry of the destinations block, with
// Dims supplied by the embedder rather than by that entry; see FromConfig.
type Options struct {
	Driver         string
	DSN            string
	Table          string
	Storage        string // halfvec | vector
	Dims           int    // the embedder's effective width
	M              int    // HNSW build parameter
	EFConstruction int    // HNSW build parameter
	EFSearch       int    // HNSW query parameter
	Granularity    string // item | fragment
	BatchSize      int
}

// normalize fills in defaults and rejects combinations that cannot work. It runs before any
// connection is opened, so a misconfigured destination fails at startup.
func (o Options) normalize() (Options, error) {
	if o.Driver == "" {
		return o, errors.New("destination driver is required")
	}
	if o.DSN == "" {
		return o, errors.New("destination DSN is required")
	}
	if o.Table == "" {
		return o, errors.New("destination table is required")
	}
	if err := checkIdentifier(o.Table); err != nil {
		return o, err
	}
	switch o.Storage {
	case StorageHalfvec, StorageVector:
	case "":
		o.Storage = StorageHalfvec
	default:
		return o, fmt.Errorf("destination storage %q is not %s or %s", o.Storage, StorageHalfvec, StorageVector)
	}
	if o.Dims <= 0 {
		return o, fmt.Errorf("destination dims must be positive, got %d", o.Dims)
	}
	switch o.Granularity {
	case GranularityItem, GranularityFragment:
	case "":
		o.Granularity = GranularityItem
	default:
		return o, fmt.Errorf("destination granularity %q is not %s or %s",
			o.Granularity, GranularityItem, GranularityFragment)
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.M <= 0 || o.EFConstruction <= 0 {
		return o, fmt.Errorf("destination hnsw.m and hnsw.ef_construction must be positive, got %d and %d",
			o.M, o.EFConstruction)
	}
	if o.EFSearch <= 0 {
		return o, fmt.Errorf("destination ef_search must be positive, got %d", o.EFSearch)
	}
	return o, nil
}

// checkIdentifier rejects a table name that cannot be safely interpolated.
//
// The table name reaches DDL and DML as text, because neither a type modifier nor a table
// name can be a bind parameter. It arrives from a configuration file rather than from a
// request, so this is not the primary defence, but a whitelist is three lines and the
// alternative is trusting every future caller of Options.
func checkIdentifier(name string) error {
	if len(name) > 63 {
		return fmt.Errorf("destination table %q exceeds PostgreSQL's 63-byte identifier limit", name)
	}
	for i, r := range name {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		switch {
		case lower, r == '_':
		case digit && i > 0:
		default:
			return fmt.Errorf("destination table %q is not a plain lowercase identifier "+
				"(letters, digits and underscore, not starting with a digit)", name)
		}
	}
	if name == "" {
		return errors.New("destination table is required")
	}
	return nil
}

// opener builds a destination from normalized options.
type opener func(ctx context.Context, opts Options) (Destination, error)

var (
	driversMu sync.RWMutex
	drivers   = map[string]opener{}
)

// Register makes a driver available to Open. It panics on a duplicate name, because two
// implementations answering to one configuration value is a build-time mistake.
func Register(name string, open opener) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if _, exists := drivers[name]; exists {
		panic("destination: driver " + name + " registered twice")
	}
	drivers[name] = open
}

// Open connects to the destination described by opts. It does not migrate and does not
// bind a model; call Migrate and AssertModel for those.
func Open(ctx context.Context, opts Options) (Destination, error) {
	opts, err := opts.normalize()
	if err != nil {
		return nil, err
	}
	driversMu.RLock()
	open, ok := drivers[opts.Driver]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown destination driver %q (registered: %v)", opts.Driver, registered())
	}
	return open(ctx, opts)
}

// registered lists the driver names, sorted so an error message is stable.
func registered() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FromConfig converts one named destination into options. Dims comes from the embedder
// because the vector width the column must hold is whatever the model actually emits after
// Matryoshka truncation, which the destination block does not state.
func FromConfig(cfg *config.Config, name string) (Options, error) {
	d, ok := cfg.Destination(name)
	if !ok {
		return Options{}, fmt.Errorf("no destination named %q is configured", name)
	}
	dsn, err := cfg.Secret(d.DSNEnv)
	if err != nil {
		return Options{}, fmt.Errorf("destination %s DSN: %w", name, err)
	}
	return Options{
		Driver:         d.Driver,
		DSN:            dsn,
		Table:          d.Table,
		Storage:        d.Storage,
		Dims:           cfg.Models.Embedder.EffectiveDims(),
		M:              d.HNSW.M,
		EFConstruction: d.HNSW.EFConstruction,
		EFSearch:       d.EFSearch,
		Granularity:    d.Granularity,
		BatchSize:      d.BatchSize,
	}, nil
}
