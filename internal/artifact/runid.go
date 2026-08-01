// Run identifiers.
//
// A run ID is a ULID (D15): its first 48 bits are a millisecond timestamp, so lexical
// ordering is chronological ordering and a reader resolves "latest" by taking the largest
// directory name that carries a _COMMIT marker. That property is the whole reason for the
// choice — no listing sort by modification time, which object stores do not offer.
//
// Generation is monotonic within a process: two runs started in the same millisecond
// still order correctly. Across processes the timestamp dominates, and two runs for the
// same source and datatype cannot legitimately overlap anyway — the state store's
// advisory lock (step 4) is what enforces that.
package artifact

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// entropy is shared and mutex-guarded because ulid.MonotonicEntropy is not safe for
// concurrent use, and because monotonicity is only meaningful against a single source.
var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

// NewRunID returns a fresh monotonic run identifier.
func NewRunID() (string, error) {
	entropyMu.Lock()
	defer entropyMu.Unlock()

	id, err := ulid.New(ulid.Timestamp(time.Now()), entropy)
	if err != nil {
		return "", fmt.Errorf("generating run id: %w", err)
	}
	return id.String(), nil
}

// ParseRunID validates a run identifier and returns the time it encodes. Readers use it
// to ignore directories that are not runs at all, so a store shared with another tool
// cannot derail run resolution.
//
// Only the canonical uppercase encoding is accepted. Crockford base32 decoding is
// case-insensitive, but "latest" is resolved by lexical comparison, and a lowercase
// directory name would sort after every uppercase one and so masquerade as the newest run.
func ParseRunID(runID string) (time.Time, error) {
	id, err := ulid.ParseStrict(runID)
	if err != nil {
		return time.Time{}, fmt.Errorf("run id %q: %w", runID, err)
	}
	if id.String() != runID {
		return time.Time{}, fmt.Errorf("run id %q: want the canonical encoding %q", runID, id.String())
	}
	return ulid.Time(id.Time()), nil
}
