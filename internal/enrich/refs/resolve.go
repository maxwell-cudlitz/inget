// Resolving one item's declared references into compose entries, metadata and edges.
//
// The per-reference digest is what makes the cascade exact: comparing the payload just pulled
// against the digest already on the edge answers "did this reference's content change" per
// reference rather than per item, which is what lets only the views depending on the changed
// reference regenerate.
//
// A change to the reference block in config needs no signature of its own. Adding a field
// changes the payload, so the digest moves; removing a reference removes its edge and its
// compose entry, so the composed hash moves. Both are already covered by D2's existing keys.
package refs

import (
	"context"
	"fmt"
	"sort"

	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// referenceTier places reference payloads first in the composed document. Composition orders
// by tier, and a reference is context an operator deliberately declared, so it should survive
// compose.max_chars truncation ahead of the source files that happened to be in scope.
const referenceTier = 0

// Input is one item's resolvable surface: where keys may be found, and how to read a fragment
// when they are found in one.
type Input struct {
	Datatype  string
	ItemID    string
	Metadata  map[string]string
	Fragments []string // every fragment key of the item
	// Content loads one fragment's raw content. It is a function so that resolution reads only
	// the blobs a key_from glob matched, and so that this package needs no artifact store.
	Content func(ctx context.Context, fragmentKey string) (string, error)
	// Stored is the item's persisted edge set, which supplies both the digests to compare
	// against and the staleness mark that says why this item is being processed.
	Stored []state.RefEdge
}

// Resolution is what one item's references contribute to its enrichment.
type Resolution struct {
	Edges       []state.RefEdge      // the item's complete edge set, for the checkpoint
	Entries     []delta.ComposeEntry // fragment-injected payloads, keyed "ref:<name>"
	Metadata    map[string]string    // metadata-injected values, keyed "<name>.<field>"
	RelatedKeys []string             // every resolved key, for the vector row's GIN column
	ChangedKeys []string             // "ref:<name>" for fragment-injected payloads that moved
	// MetadataChanged reports that a metadata-injected payload moved. It is a flag rather than
	// a key because metadata reaches every prompt, so no glob can scope it.
	MetadataChanged bool
	// Digest is the metadata-injected payload's contribution to every view's level-2 input
	// hash. Fragment-injected payloads need no equivalent: they are in the document.
	Digest string
	// Depth is the cascade depth this item was invalidated at, or 0 when it is being processed
	// for its own sake.
	Depth int
}

// Set is one datatype's configured references, ready to resolve. It is safe for concurrent
// use: nothing in it is mutated after New returns.
type Set struct {
	refs []reference
}

// reference is one config entry bound to its resolver.
type reference struct {
	name     string
	kind     string
	fields   []string
	injectAs string
	datatype string // inget: the referenced datatype, part of the edge key
	key      *keyExtractor
	resolver Resolver
}

// New builds the reference set of one datatype, returning nil when it declares none. The
// secret function resolves an http reference's token_env, so this package reads no
// environment of its own.
func New(refs []config.Reference, store Store, secret func(config.SecretRef) (string, error)) (*Set, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	set := &Set{refs: make([]reference, 0, len(refs))}
	for _, r := range refs {
		bound, err := bind(r, store, secret)
		if err != nil {
			return nil, err
		}
		set.refs = append(set.refs, bound)
	}
	return set, nil
}

// bind compiles one config entry and opens its resolver.
func bind(r config.Reference, store Store, secret func(config.SecretRef) (string, error)) (reference, error) {
	key, err := newKeyExtractor(r.KeyFrom, r.KeyRegex)
	if err != nil {
		return reference{}, fmt.Errorf("reference %s: %w", r.Name, err)
	}
	var token string
	if r.TokenEnv != "" {
		if token, err = secret(r.TokenEnv); err != nil {
			return reference{}, fmt.Errorf("reference %s token: %w", r.Name, err)
		}
	}
	resolver, err := Open(Options{
		Name:     r.Name,
		Kind:     r.Resolver,
		Datatype: r.Datatype,
		Endpoint: r.Endpoint,
		Token:    token,
		Store:    store,
	})
	if err != nil {
		return reference{}, err
	}
	return reference{
		name:     r.Name,
		kind:     r.Resolver,
		fields:   r.Fields,
		injectAs: r.InjectAs,
		datatype: r.Datatype,
		key:      key,
		resolver: resolver,
	}, nil
}

// Resolve looks up every reference of one item.
func (s *Set) Resolve(ctx context.Context, in Input) (Resolution, error) {
	prior, depth := indexStored(in.Stored)
	res := Resolution{Depth: depth, Metadata: map[string]string{}}

	var metadataDigests []string
	for _, ref := range s.refs {
		current, records, err := ref.resolveAll(ctx, in, &res)
		if err != nil {
			return Resolution{}, err
		}
		changed := !sameDigests(prior[ref.name], current)

		if ref.injectAs == InjectMetadata {
			ref.injectMetadata(res.Metadata, records)
			metadataDigests = append(metadataDigests, digestOf(ref.name, current))
			res.MetadataChanged = res.MetadataChanged || changed
			continue
		}
		if len(records) > 0 {
			res.Entries = append(res.Entries, delta.ComposeEntry{
				Key:     EntryPrefix + ref.name,
				Tier:    referenceTier,
				Content: ref.render(records),
			})
		}
		if changed {
			res.ChangedKeys = append(res.ChangedKeys, EntryPrefix+ref.name)
		}
	}
	sort.Strings(res.RelatedKeys)
	res.Digest = combine("refs", metadataDigests...)
	return res, nil
}

// resolveAll extracts one reference's keys, resolves each, and records the edge and related
// key of every one. The digest map it returns is what the change comparison reads; the
// records are what injection reads, and hold only the referents that resolved.
func (r reference) resolveAll(ctx context.Context, in Input, res *Resolution) (map[string]string, []Record, error) {
	keys, err := r.key.extract(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("reference %s of %s/%s: %w", r.name, in.Datatype, in.ItemID, err)
	}
	current := make(map[string]string, len(keys))
	records := make([]Record, 0, len(keys))
	for _, key := range keys {
		rec, err := r.resolver.Resolve(ctx, key, r.fields)
		if err != nil {
			return nil, nil, fmt.Errorf("reference %s of %s/%s: %w", r.name, in.Datatype, in.ItemID, err)
		}
		edgeKey := r.edgeKey(key)
		digest := digestRecord(r.name, edgeKey, rec)
		current[edgeKey] = digest
		res.Edges = append(res.Edges, state.RefEdge{
			Name: r.name, Kind: r.kind, Key: edgeKey, Fingerprint: digest,
		})
		res.RelatedKeys = append(res.RelatedKeys, edgeKey)
		if rec.resolved() {
			records = append(records, rec)
		}
	}
	return current, records, nil
}
