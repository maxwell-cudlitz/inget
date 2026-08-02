// Package source is the connector surface inget-fetch enumerates and fetches through.
//
// A connector knows one third-party API and nothing else: not the blob store, not the
// state store, not the artifact envelope. It answers two questions — what items exist, and
// what one item consists of — and internal/fetch turns those answers into a committed run.
// That split is what lets a new source be one directory plus one Register call.
//
//	source.go    the types and the Connector interface
//	registry.go  the driver registry and the bridge from configuration
//
// Fragment tiers are declared here rather than per driver because they are part of the
// artifact envelope's contract with the consumer: the composer orders by tier, so two
// sources disagreeing about what tier 0 means would compose incomparable documents.
package source

import (
	"context"
	"errors"
	"time"
)

// Composition priority tiers, in the order a composer emits them. The envelope
// specification fixes both the numbers and their meanings.
const (
	TierDocs        = 0 // README, docs/, design notes: what the item says about itself
	TierEntrypoints = 1 // main, cmd/, index: where execution starts
	TierConfig      = 2 // manifests, Dockerfiles, CI, infrastructure declarations
	TierSource      = 3 // implementation
	TierOther       = 4 // everything else that survived filtering
)

// ErrStopList tells List to stop enumerating. A yield function returns it when the caller
// has all it asked for — a --limit reached, or a context it would rather cancel cleanly —
// and List returns it unchanged so the caller can recognize its own signal.
var ErrStopList = errors.New("enumeration stopped by caller")

// Ref identifies one source item plus a cheap change token. An empty Fingerprint means
// "unknown", which obliges the caller to fetch the item.
type Ref struct {
	ID          string
	Fingerprint string
}

// Item is the identity and metadata of one record. Metadata is a flat string map because
// that is what the envelope carries and what a prompt template can address; it is
// third-party input on its way into an LLM prompt and is never trusted.
type Item struct {
	ID          string
	Fingerprint string
	Metadata    map[string]string
}

// Fragment is an addressable sub-unit of an item: a file, a column value, an update.
//
// Bytes is the size of the source content, which is not always len(Content). A fragment
// whose content was withheld — a secret was detected in it, or it exceeded the blob size
// cap — still reports how big the thing at the source is, so the record describes the
// repository rather than describing what this run chose to upload.
type Fragment struct {
	Key         string            // stable within the item: cmd/root.go, README.md#1
	Fingerprint string            // level-1 change token; a git blob SHA where one is free
	Content     []byte            // nil when the content is deliberately not carried
	Bytes       int64             // size at the source
	Tier        int               // composition priority; one of the Tier constants
	MIME        string            // best-effort content type
	Truncated   bool              // content exceeded the per-fragment size cap
	Meta        map[string]string // datatype-specific extras, e.g. excluded=secret
}

// ListQuery scopes enumeration. Only makes a run partial by definition: the caller named
// the items, so absence of anything else implies nothing.
type ListQuery struct {
	Only  []string  // explicit item IDs
	Since time.Time // source-side filter, honoured where the API supports one
	Limit int       // stop after this many items; 0 means no cap
}

// Result is one fetched item.
//
// Warnings are the per-item problems that are not failures: a repository whose tree the
// API truncated, a file excluded because a secret was found in it, an archive that hit its
// size cap. They reach the manifest, because a run that quietly dropped half a repository
// and a run that fetched all of it must not look the same to whoever reads it later.
type Result struct {
	Item      Item
	Fragments []Fragment
	Warnings  []string
}

// Connector enumerates and fetches items for one source.
//
// List streams refs through yield so a domain of half a million items is never
// materialized; it returns ErrStopList unchanged when yield asks it to stop. Fetch is
// called concurrently for different refs, so an implementation must be safe for concurrent
// use.
type Connector interface {
	Name() string
	Datatypes() []string
	List(ctx context.Context, datatype string, q ListQuery, yield func(Ref) error) error
	Fetch(ctx context.Context, datatype string, ref Ref) (Result, error)
	Close() error
}

// EventMapper is the optional interface a connector implements when its source can deliver
// a webhook. --event-file reads the payload and this turns it into the item IDs to fetch.
//
// It is optional rather than part of Connector because a source without webhooks would
// otherwise have to carry a method that always fails, and a caller could not tell that
// from one whose payload it simply did not understand.
type EventMapper interface {
	ItemIDsFromEvent(payload []byte) ([]string, error)
}
