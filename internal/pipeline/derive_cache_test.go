// A stale outer cache miss must not spend a second derivation after another worker persisted one.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sync/singleflight"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

func TestDeriveFragmentRechecksStaleMissBeforeGenerating(t *testing.T) {
	h := newCascadeHarness(t)
	digest, _, err := h.arts.PutBlob(h.ctx, []byte("original content"))
	if err != nil {
		t.Fatal(err)
	}
	frag := artifact.Fragment{Key: "README.md", Fingerprint: "fp", Blob: digest}
	sig := h.deps.FragEnricher.Signature()
	key := delta.FragmentCacheKey(frag.Key, frag.Fingerprint, sig)
	wrapped := &staleMissStore{Store: h.store, derivation: state.Derivation{
		CacheKey: key, Datatype: testDatatype, ItemID: testItemID, FragKey: frag.Key,
		Signature: sig, Output: "another worker's summary",
	}}
	ex := &execution{
		deps: h.deps, arts: h.arts, derive: &singleflight.Group{},
		fragSig: sig, gen: newLimiter(1), stats: &statsCollector{},
	}
	ex.deps.State = wrapped
	got, err := deriveFragment(h.ctx, ex, testItemID, frag)
	if err != nil || got != wrapped.derivation.Output {
		t.Fatalf("deriveFragment = %q, %v; want the concurrently persisted output", got, err)
	}
	if h.gen.Calls() != 0 {
		t.Errorf("generated %d times after the outer miss became stale", h.gen.Calls())
	}
	if wrapped.reads != 2 {
		t.Errorf("cache reads = %d, want outer read and protected recheck", wrapped.reads)
	}
}

func TestDeriveFragmentPropagatesProtectedCacheFailureWithoutGeneration(t *testing.T) {
	h := newCascadeHarness(t)
	wantErr := errors.New("cache unavailable")
	wrapped := &staleMissStore{Store: h.store, recheckErr: wantErr}
	ex := &execution{deps: h.deps, arts: h.arts, derive: &singleflight.Group{},
		fragSig: h.deps.FragEnricher.Signature(), gen: newLimiter(1), stats: &statsCollector{}}
	ex.deps.State = wrapped
	_, err := deriveFragment(h.ctx, ex, testItemID, artifact.Fragment{Key: "README.md", Fingerprint: "fp", Blob: "unused"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("deriveFragment error = %v, want cache failure", err)
	}
	if h.gen.Calls() != 0 {
		t.Error("generated after the cache recheck failed")
	}
}

// staleMissStore simulates a worker publishing after the first lookup observed a miss.
// Publishing before returning that miss makes the race deterministic without sleeps or hooks.
type staleMissStore struct {
	state.Store
	derivation state.Derivation
	recheckErr error
	reads      int
}

func (s *staleMissStore) Derivation(ctx context.Context, key string) (string, bool, error) {
	s.reads++
	if s.reads == 1 {
		if s.derivation.CacheKey != "" {
			if err := s.PutDerivation(ctx, s.derivation); err != nil {
				return "", false, fmt.Errorf("simulating concurrent derivation: %w", err)
			}
		}
		return "", false, nil
	}
	if s.recheckErr != nil {
		return "", false, s.recheckErr
	}
	output, found, err := s.Store.Derivation(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("reading published derivation: %w", err)
	}
	return output, found, nil
}
