// Package fetch turns a connector's answers into one committed artifact run.
//
// It owns everything a connector deliberately does not: the datatype lock, the level-0 skip
// against persisted state, the content-addressed blob writes, shard rolling, tombstone
// computation, and the commit protocol. A connector answers "what items exist" and "what is in
// this one"; this package decides what any of that costs and what becomes visible.
//
//	fetch.go  the dependencies, the run configuration and the report
//	run.go    enumeration, the worker pool, tombstones and the commit
//	item.go   one item into one record, including which blobs still need writing
//
// Two rules govern the whole package. Enumeration streams, so a domain of half a million items
// is never materialized beyond the set of identifiers tombstones need. And a failure to fetch
// one item is that item's problem: it is warned about and counted, and because the item was
// still enumerated it produces no tombstone, so a transient error cannot delete anything.
package fetch

import (
	"errors"
	"time"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/source"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// Deps are the collaborators a run needs. State is read for level-0 fingerprints and prior blob
// references and written only for run history; every durable artifact goes to Artifacts.
type Deps struct {
	State     state.Store
	Artifacts *artifact.Store
	Connector source.Connector
}

// RunConfig is one invocation.
//
// Scope is load-bearing (D6): only a full, untruncated run may issue tombstones, so Only, Since
// and an event file all force partial. Anything else would let a webhook carrying one changed
// repository be read as "every other repository was deleted".
type RunConfig struct {
	Source   string
	Datatype string
	Scope    artifact.Scope

	Only  []string
	Since time.Time
	Limit int

	DryRun      bool
	Concurrency int

	MaxFragmentsPerItem int
	DomainHash          string
	ConfigHash          string
	Producer            string // name/version of the writing binary, for the manifest

	// Binary is what the run history records, which is a plain program name rather than the
	// versioned producer string: ResumableRun matches on it, so a version bump must not make
	// yesterday's runs belong to a different program.
	Binary string
}

// Defaults for a run whose configuration leaves them unset.
const (
	defaultConcurrency = 4

	// DefaultBinary is the program name the run history records for a fetch.
	DefaultBinary = "inget-fetch"

	// maxWarnings bounds the manifest's warnings array. A manifest is read by a person, and
	// a thousand lines of it is read by nobody; the count in the report stays exact.
	maxWarnings = 200
)

// normalize fills in defaults and rejects a configuration that cannot produce a valid manifest.
func (rc *RunConfig) normalize() error {
	switch {
	case rc.Source == "":
		return errors.New("fetch requires a source name")
	case rc.Datatype == "":
		return errors.New("fetch requires a datatype")
	case rc.Producer == "":
		return errors.New("fetch requires a producer name")
	case rc.DomainHash == "" || rc.ConfigHash == "":
		return errors.New("fetch requires the domain and config hashes")
	}
	if rc.Concurrency <= 0 {
		rc.Concurrency = defaultConcurrency
	}
	if rc.Binary == "" {
		rc.Binary = DefaultBinary
	}
	if len(rc.Only) > 0 || !rc.Since.IsZero() {
		rc.Scope = artifact.ScopePartial
	}
	switch rc.Scope {
	case artifact.ScopeFull, artifact.ScopePartial:
	case "":
		rc.Scope = artifact.ScopeFull
	default:
		return errors.New("fetch scope must be full or partial")
	}
	return nil
}

// lockKey is the state lock a fetch takes. It is distinct from the datatype name that inget
// locks: a fetch only reads the guard state, so blocking an enrichment run that could be
// consuming an earlier artifact would serialize two jobs that do not conflict.
func (rc RunConfig) lockKey() string { return "fetch:" + rc.Datatype }

// Report is what a run did. It is the manifest's counts plus the numbers only the producer sees:
// how many items were enumerated, how many were skipped as unchanged, and how many failed.
type Report struct {
	RunID    string `json:"run_id"`
	Source   string `json:"source"`
	Datatype string `json:"datatype"`
	Scope    string `json:"scope"`
	DryRun   bool   `json:"dry_run"`

	Enumerated       int `json:"enumerated"`
	Items            int `json:"items"`
	SkippedUnchanged int `json:"items_skipped_unchanged"`
	Failed           int `json:"items_failed"`
	Fragments        int `json:"fragments"`
	BlobsWritten     int `json:"blobs_written"`
	BlobsReused      int `json:"blobs_reused"`
	Tombstones       int `json:"tombstones"`

	Truncated bool     `json:"truncated"`
	Warnings  []string `json:"warnings,omitempty"`
}
