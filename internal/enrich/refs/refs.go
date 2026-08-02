// Package refs resolves the cross-record and external references a datatype declares, and
// carries the reverse-dependency invalidation that keeps them honest (D12).
//
// A reference is an enrichment input, so signature completeness (D2) applies to it: if a
// referenced record changes and nothing in a cache key moves, the referencing view stays
// stale forever. Two mechanisms cover that, and neither needs a new hash of its own:
//
//   - A reference injected as a fragment becomes a compose entry keyed "ref:<name>", so it
//     flows through the existing level-2 input hash and the existing depends_on globs decide
//     which views depend on it. Untrusted third-party text therefore lands inside the
//     composed document, which is where the view prompts already fence it off.
//   - A reference injected as metadata cannot be scoped by a glob, because every prompt
//     receives the whole metadata map. Its payload contributes a digest to every view's
//     level-2 input hash instead.
//
// Resolution is single-hop by construction. The inget resolver reads persisted state, and a
// referent's persisted views were themselves generated with its own references injected, so
// transitive context is already present without this package recursing. Depth bounds the
// invalidation cascade rather than the lookup; see cascade.go.
package refs

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/maxwell-cudlitz/inget/internal/state"
)

// Resolver kinds, matching the datatypes[].references[].resolver vocabulary in config.
const (
	KindInget = "inget"
	KindHTTP  = "http"
)

// Injection modes, matching the inject_as vocabulary in config.
const (
	InjectFragment = "fragment"
	InjectMetadata = "metadata"
)

// EntryPrefix names a reference's compose entry. It is a prefix rather than a bare name so
// that a view's depends_on can address references as a group ("ref:**") and so that a
// reference can never collide with a fragment key.
const EntryPrefix = "ref:"

// Record is one resolved referent: the key it was found under and the allowlisted fields
// that came back. An empty Fields map means unresolved — either the referent does not exist
// yet or it held none of the requested fields — and both read the same downstream, because
// both inject nothing.
type Record struct {
	Key    string
	Fields map[string]string
}

// resolved reports whether the record carries anything worth injecting.
func (r Record) resolved() bool { return len(r.Fields) > 0 }

// Resolver fetches a referenced record by key. Implementations are read-only, idempotent and
// safe for concurrent use: the pipeline resolves references from every item worker at once.
type Resolver interface {
	// Kind returns the registered resolver name.
	Kind() string

	// Resolve looks up one key and returns only the requested fields. A missing referent is
	// not an error: it is an empty Record, because a Monday item pointing at a repository
	// inget has not fetched yet is ordinary and must not fail the item.
	Resolve(ctx context.Context, key string, fields []string) (Record, error)
}

// Store is the slice of state.Store this package needs. It is declared here, by the
// consumer, so that a resolver test needs a stub of three methods rather than of the whole
// state surface.
type Store interface {
	Item(ctx context.Context, datatype, itemID string) (state.Item, bool, error)
	ViewState(ctx context.Context, datatype, itemID string) (map[string]state.ViewState, error)
	MarkRefsStale(ctx context.Context, kind, key string, depth int) (int, error)
}

// Options configures one resolver instance. A reference owns its resolver rather than
// sharing one per kind, because endpoint and credential are per reference.
type Options struct {
	Name     string       // reference name, for error messages
	Kind     string       // registered resolver name
	Datatype string       // inget: the referenced datatype
	Endpoint string       // http: URL template, ${ENV} expanded, {key} substituted
	Token    string       // http: bearer credential, already resolved from its env var
	Store    Store        // inget: where persisted records are read from
	Client   *http.Client // http: optional, for tests
}

// opener builds a resolver from options.
type opener func(opts Options) (Resolver, error)

var (
	resolversMu sync.RWMutex
	resolvers   = map[string]opener{}
)

// Register makes a resolver available to Open. It panics on a duplicate name, because two
// implementations answering to one configuration value is a build-time mistake and not a
// condition any run could recover from.
func Register(kind string, open opener) {
	resolversMu.Lock()
	defer resolversMu.Unlock()
	if _, exists := resolvers[kind]; exists {
		panic("refs: resolver " + kind + " registered twice")
	}
	resolvers[kind] = open
}

// Open builds the resolver described by opts.
func Open(opts Options) (Resolver, error) {
	if opts.Kind == "" {
		return nil, fmt.Errorf("reference %q has no resolver", opts.Name)
	}
	resolversMu.RLock()
	open, ok := resolvers[opts.Kind]
	resolversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown reference resolver %q (registered: %v)", opts.Kind, Registered())
	}
	resolver, err := open(opts)
	if err != nil {
		return nil, fmt.Errorf("opening resolver for reference %s: %w", opts.Name, err)
	}
	return resolver, nil
}

// Registered lists the resolver kinds, sorted so an error message is stable.
func Registered() []string {
	resolversMu.RLock()
	defer resolversMu.RUnlock()
	kinds := make([]string, 0, len(resolvers))
	for kind := range resolvers {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
