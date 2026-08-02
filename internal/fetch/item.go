// One item into one record, and the decision that makes an incremental fetch cheap: which
// fragments still need their content uploaded.
//
// A fragment whose level-1 fingerprint matches what state recorded already has its content in the
// blob store under the digest state remembers, so the reference is reused and nothing is
// transferred. That is safe by contract: garbage collection never deletes a blob referenced by
// live state (docs/artifact-envelope.md), so a remembered digest is a present blob.
//
// Everything else goes to PutBlob, which hashes the content and checks for existence before
// writing. Identical content across repositories — the same licence header, the same generated
// client — is therefore stored once no matter how many items reference it.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/source"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// fetchItem fetches one item and writes its record.
//
// A connector failure is this item's problem: it is warned about, counted, and the run continues.
// Because the item was still enumerated it appears in the seen set, so no tombstone is issued for
// something that merely failed to download. An artifact or state failure is returned, because it
// is infrastructure and every remaining item would fail the same way.
func (r *runner) fetchItem(ctx context.Context, ref source.Ref) error {
	result, err := r.deps.Connector.Fetch(ctx, r.cfg.Datatype, ref)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		slog.WarnContext(ctx, "fetch failed", "datatype", r.cfg.Datatype, "item", ref.ID, "error", err.Error())
		r.warn(fmt.Sprintf("%s: fetch failed: %s", ref.ID, err))
		r.mu.Lock()
		r.failed++
		r.mu.Unlock()
		return nil
	}
	for _, line := range result.Warnings {
		r.warn(line)
	}

	record, err := r.record(ctx, result)
	if err != nil {
		return err
	}
	if err := r.writer.Write(ctx, record); err != nil {
		return fmt.Errorf("writing record for %s: %w", record.ItemID, err)
	}

	r.mu.Lock()
	r.items++
	r.fragments += len(record.Fragments)
	r.mu.Unlock()
	slog.DebugContext(ctx, "fetched item",
		"datatype", r.cfg.Datatype, "item", record.ItemID, "fragments", len(record.Fragments))
	return nil
}

// record turns a fetch result into an artifact record, writing whatever blobs are missing.
func (r *runner) record(ctx context.Context, result source.Result) (*artifact.Record, error) {
	prior, err := r.deps.State.Fragments(ctx, r.cfg.Datatype, result.Item.ID)
	if err != nil {
		return nil, err
	}

	// The cap is applied before any upload, so content that is about to be dropped is never
	// transferred to the store. The connector sorted fragments by tier, so the prefix that
	// survives is the informative one rather than an alphabetical accident.
	total := len(result.Fragments)
	fragments := result.Fragments
	capped := false
	if max := r.cfg.MaxFragmentsPerItem; max > 0 && total > max {
		fragments = fragments[:max]
		capped = true
		r.warn(fmt.Sprintf("%s: fragment count %d exceeded max_fragments_per_item=%d",
			result.Item.ID, total, max))
	}

	out := make([]artifact.Fragment, 0, len(fragments))
	for _, f := range fragments {
		converted, err := r.fragment(ctx, prior, f)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return &artifact.Record{
		ItemID:            result.Item.ID,
		Fingerprint:       result.Item.Fingerprint,
		FetchedAt:         time.Now().UTC().Truncate(time.Second),
		Metadata:          result.Item.Metadata,
		Fragments:         out,
		FragmentCount:     total,
		FragmentTruncated: capped,
	}, nil
}

// fragment converts one source fragment, resolving its blob reference.
func (r *runner) fragment(ctx context.Context, prior map[string]state.FragmentState, f source.Fragment) (artifact.Fragment, error) {
	out := artifact.Fragment{
		Key:         f.Key,
		Fingerprint: f.Fingerprint,
		Bytes:       f.Bytes,
		Tier:        f.Tier,
		MIME:        f.MIME,
		Truncated:   f.Truncated,
		Meta:        f.Meta,
	}
	if len(f.Content) == 0 {
		// Content the connector deliberately withheld — a detected secret, a file over the
		// split budget — or a genuinely empty file. Either way there is no blob to write.
		return out, nil
	}
	if known, ok := prior[f.Key]; ok && known.BlobRef != "" && known.Fingerprint == f.Fingerprint && f.Fingerprint != "" {
		out.Blob = known.BlobRef
		r.mu.Lock()
		r.blobsReused++
		r.mu.Unlock()
		return out, nil
	}

	digest, written, err := r.deps.Artifacts.PutBlob(ctx, f.Content)
	if err != nil {
		if errors.Is(err, artifact.ErrBlobTooLarge) {
			out.Truncated = true
			return out, nil
		}
		return artifact.Fragment{}, fmt.Errorf("storing content of %s: %w", f.Key, err)
	}
	out.Blob = digest
	r.mu.Lock()
	if written {
		r.blobsWritten++
	} else {
		r.blobsReused++
	}
	r.mu.Unlock()
	return out, nil
}
