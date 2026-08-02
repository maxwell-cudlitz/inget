// The inget resolver: cross-record references into inget's own state.
//
// Fields name what to pull from the referenced record. A bare name is a metadata field and
// "view:<name>" is one of the referent's generated views. Both come from the state store
// rather than from a destination, even though a destination holds the same view text: the
// text in state is the text the stored vector was produced from, so a round trip through
// pgvector would return the same string while adding a method to the Destination interface
// and requiring a reachable vector database during enrichment — which `inget plan`, which
// opens no destination, deliberately does not have.
package refs

import (
	"context"
	"fmt"
	"strings"
)

// viewPrefix marks a field that reads a generated view rather than item metadata.
const viewPrefix = "view:"

func init() { Register(KindInget, newIngetResolver) }

// ingetResolver reads persisted records of one datatype.
type ingetResolver struct {
	datatype string
	store    Store
}

// newIngetResolver builds the resolver for one reference.
func newIngetResolver(opts Options) (Resolver, error) {
	if opts.Datatype == "" {
		return nil, fmt.Errorf("resolver %s needs a datatype", KindInget)
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("resolver %s needs a state store", KindInget)
	}
	return &ingetResolver{datatype: opts.Datatype, store: opts.Store}, nil
}

// Kind implements Resolver.
func (r *ingetResolver) Kind() string { return KindInget }

// Resolve implements Resolver. A referent inget has not fetched yet resolves to an empty
// record, which the caller records as an unresolved edge: the edge is what lets the referent's
// first run invalidate this item once it does arrive.
func (r *ingetResolver) Resolve(ctx context.Context, key string, fields []string) (Record, error) {
	rec := Record{Key: key}
	item, found, err := r.store.Item(ctx, r.datatype, key)
	if err != nil {
		return Record{}, fmt.Errorf("reading referenced item %s/%s: %w", r.datatype, key, err)
	}
	if !found {
		return rec, nil
	}

	var views map[string]viewText
	if wantsView(fields) {
		views, err = r.viewTexts(ctx, key)
		if err != nil {
			return Record{}, err
		}
	}

	rec.Fields = make(map[string]string, len(fields))
	for _, field := range fields {
		if name, ok := strings.CutPrefix(field, viewPrefix); ok {
			if v, ok := views[name]; ok && v.text != "" {
				rec.Fields[field] = v.text
			}
			continue
		}
		if value := item.Metadata[field]; value != "" {
			rec.Fields[field] = value
		}
	}
	if len(rec.Fields) == 0 {
		rec.Fields = nil // an item holding none of the requested fields is unresolved
	}
	return rec, nil
}

// viewText is one referenced view's text.
type viewText struct{ text string }

// viewTexts reads the referent's generated views.
func (r *ingetResolver) viewTexts(ctx context.Context, key string) (map[string]viewText, error) {
	states, err := r.store.ViewState(ctx, r.datatype, key)
	if err != nil {
		return nil, fmt.Errorf("reading views of referenced item %s/%s: %w", r.datatype, key, err)
	}
	texts := make(map[string]viewText, len(states))
	for name, v := range states {
		texts[name] = viewText{text: v.Text}
	}
	return texts, nil
}

// wantsView reports whether any requested field reads a view, so that the view query is
// skipped for a reference that only pulls metadata.
func wantsView(fields []string) bool {
	for _, f := range fields {
		if strings.HasPrefix(f, viewPrefix) {
			return true
		}
	}
	return false
}
