// The real fetch producer and enrichment consumer share one artifact/state store here.
// Source responses are controlled, but the commit, skip, failure, tombstone and deletion
// paths are production code: the boundary must preserve items no record was written for.
package pipeline

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/fetch"
	"github.com/maxwell-cudlitz/inget/internal/source"
)

func TestIncrementalFullFetchPreservesSkippedAndFailedItems(t *testing.T) {
	h := newCascadeHarness(t)
	ids := []string{"owner/unchanged", "owner/failed", "owner/gone", "owner/active"}
	h.writeRunWithItems(baseVersion, noChange, "", ids...)
	if _, _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.reset()
	unchanged := source.Ref{
		ID: "owner/unchanged", Fingerprint: recordFingerprint("owner/unchanged", baseVersion, noChange, ""),
	}
	active := source.Ref{ID: "owner/active", Fingerprint: "v2"}
	content := []byte("Updated active repository documentation.")
	conn := &deletionConnector{
		refs: []source.Ref{unchanged, {ID: "owner/failed", Fingerprint: "v2"}, active},
		result: source.Result{
			Item: source.Item{ID: active.ID, Fingerprint: active.Fingerprint,
				Metadata: map[string]string{"full_name": active.ID}},
			Fragments: []source.Fragment{{
				Key: "README.md", Fingerprint: sha256Hex(string(content)), Content: content,
				Bytes: int64(len(content)), Tier: source.TierDocs,
			}},
		},
	}
	report, err := fetch.Run(h.ctx, fetch.Deps{
		State: h.store, Artifacts: h.arts, Connector: conn,
	}, fetch.RunConfig{
		Source: testSource, Datatype: testDatatype, Scope: artifact.ScopeFull,
		Producer: "test-fetch", DomainHash: "sha256:domain", ConfigHash: testConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Enumerated != 3 || report.SkippedUnchanged != 1 || report.Failed != 1 || report.Items != 1 || report.Tombstones != 1 {
		t.Fatalf("fetch report does not describe the intended boundary case: %+v", report)
	}
	artifactRun, err := h.arts.OpenRun(h.ctx, testSource, testDatatype, report.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(artifactRun.Manifest.Tombstones, []string{"owner/gone"}) {
		t.Fatalf("producer tombstones = %v, want only owner/gone", artifactRun.Manifest.Tombstones)
	}
	plan, err := h.plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Deleted != 1 || !slices.Equal(plan.WorkItems, []string{active.ID}) {
		t.Fatalf("consumer plan deletes=%d work=%v, want one delete and active item", plan.Deleted, plan.WorkItems)
	}
	_, stats, err := h.run()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TombstonesApplied != 1 || stats.ItemsProcessed != 1 || !slices.Equal(h.dest.deleted, []string{"owner/gone"}) {
		t.Fatalf("consumer applied=%d processed=%d calls=%v", stats.TombstonesApplied, stats.ItemsProcessed, h.dest.deleted)
	}
	for _, id := range []string{unchanged.ID, "owner/failed", active.ID} {
		assertDeletionItem(t, h, id, true)
		views, err := h.store.ViewState(h.ctx, testDatatype, id)
		if err != nil || len(views) != numViews {
			t.Fatalf("retained views for %s = %d, %v; want %d", id, len(views), err, numViews)
		}
	}
	assertDeletionItem(t, h, "owner/gone", false)
}

type deletionConnector struct {
	refs   []source.Ref
	result source.Result
}

func (*deletionConnector) Name() string        { return testSource }
func (*deletionConnector) Datatypes() []string { return []string{testDatatype} }
func (*deletionConnector) Close() error        { return nil }

func (c *deletionConnector) List(_ context.Context, _ string, _ source.ListQuery, yield func(source.Ref) error) error {
	for _, ref := range c.refs {
		if err := yield(ref); err != nil {
			return err
		}
	}
	return nil
}

func (c *deletionConnector) Fetch(_ context.Context, _ string, ref source.Ref) (source.Result, error) {
	if ref.ID != c.result.Item.ID {
		return source.Result{}, fmt.Errorf("temporary source failure for %s", ref.ID)
	}
	return c.result, nil
}
