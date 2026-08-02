// The write paths: incremental upsert, staged bulk load, and item deletion.
//
// Both write paths pass through binding.check first, so D7 is enforced before any row
// reaches the database, and both send the embedding as text cast server-side to the
// column's type; see row.go for why.
package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// columns is the insert column list, shared by both write paths so they cannot drift.
var columns = []string{
	"id", "datatype", "item_id", "view_name", "granularity", "frag_key",
	"text", "embedding", "model", "dims", "signature", "metadata", "related_keys",
}

// conflictUpdate is the DO UPDATE clause. Every column except the identity is refreshed,
// including updated_at, so a re-upsert is observable even when the vector is unchanged.
const conflictUpdate = `ON CONFLICT (id) DO UPDATE SET
	granularity  = EXCLUDED.granularity,
	frag_key     = EXCLUDED.frag_key,
	text         = EXCLUDED.text,
	embedding    = EXCLUDED.embedding,
	model        = EXCLUDED.model,
	dims         = EXCLUDED.dims,
	signature    = EXCLUDED.signature,
	metadata     = EXCLUDED.metadata,
	related_keys = EXCLUDED.related_keys,
	updated_at   = now()`

// Upsert writes rows in batches of the configured size.
//
// Each batch is one round trip carrying batch_size statements; pgx prepares the statement
// once and reuses the plan. A batch is not a transaction, so a failure partway leaves
// earlier batches written — which is what the pipeline's per-item checkpointing expects,
// since an item is checkpointed only after its rows land.
func (p *pgvectorStore) Upsert(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	bound := p.binding()
	if err := bound.check(rows); err != nil {
		return fmt.Errorf("upserting into %s: %w", p.opts.Table, err)
	}
	statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) %s",
		p.opts.Table, strings.Join(columns, ", "), p.placeholders(), conflictUpdate)

	for start := 0; start < len(rows); start += p.opts.BatchSize {
		end := min(start+p.opts.BatchSize, len(rows))
		batch := &pgx.Batch{}
		for _, r := range rows[start:end] {
			args, err := rowArgs(r)
			if err != nil {
				return fmt.Errorf("upserting into %s: %w", p.opts.Table, err)
			}
			batch.Queue(statement, args...)
		}
		if err := p.pool.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("upserting %d rows into %s: %w", end-start, p.opts.Table, err)
		}
	}
	return nil
}

// placeholders renders $1..$N with the embedding cast to the column's vector type. The cast
// is what lets the text encoding reach a halfvec column.
func (p *pgvectorStore) placeholders() string {
	parts := make([]string, len(columns))
	for i, name := range columns {
		parts[i] = fmt.Sprintf("$%d", i+1)
		if name == "embedding" {
			parts[i] += p.vectorCast()
		}
	}
	return strings.Join(parts, ", ")
}

// BulkLoad copies rows into a staging table and merges them in one statement.
//
// COPY does not support ON CONFLICT, so the merge is a second statement over a temporary
// table. The staging table's embedding column is text: COPY then carries exactly the bytes
// the text encoding produces, and the cast to halfvec happens once, server-side, in the
// merge. That removes the need for binary halfvec support in the client entirely.
func (p *pgvectorStore) BulkLoad(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	bound := p.binding()
	if err := bound.check(rows); err != nil {
		return fmt.Errorf("bulk loading into %s: %w", p.opts.Table, err)
	}
	encoded := make([][]any, len(rows))
	for i, r := range rows {
		args, err := rowArgs(r)
		if err != nil {
			return fmt.Errorf("bulk loading into %s: %w", p.opts.Table, err)
		}
		encoded[i] = args
	}

	return p.inTx(ctx, func(tx pgx.Tx) error {
		const staging = "inget_bulk_load"
		// LIKE copies the column names, types and NOT NULL constraints; INCLUDING DEFAULTS
		// is needed as well, because updated_at is NOT NULL with a default and COPY does
		// not supply it. Indexes and the primary key are deliberately not copied: nothing
		// queries the staging table, and binding.check has already refused a batch with a
		// repeated id, which is the only thing a unique constraint here would catch.
		//
		// The vector column is then retyped to text so COPY never has to speak halfvec.
		// ON COMMIT DROP means no cleanup path and no collision between two concurrent
		// loads, which hold separate sessions.
		ddl := fmt.Sprintf(`CREATE TEMP TABLE %s (LIKE %s INCLUDING DEFAULTS) ON COMMIT DROP;
			ALTER TABLE %s ALTER COLUMN embedding TYPE text;`, staging, p.opts.Table, staging)
		if _, err := tx.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("creating the staging table for %s: %w", p.opts.Table, err)
		}
		copied, err := tx.CopyFrom(ctx, pgx.Identifier{staging}, columns,
			pgx.CopyFromRows(encoded))
		if err != nil {
			return fmt.Errorf("copying %d rows into the staging table for %s: %w",
				len(encoded), p.opts.Table, err)
		}
		if copied != int64(len(encoded)) {
			return fmt.Errorf("staging table for %s took %d of %d rows", p.opts.Table, copied, len(encoded))
		}
		merge := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s %s`,
			p.opts.Table, strings.Join(columns, ", "), p.selectList(), staging, conflictUpdate)
		if _, err := tx.Exec(ctx, merge); err != nil {
			return fmt.Errorf("merging the staging table into %s: %w", p.opts.Table, err)
		}
		return nil
	})
}

// selectList reads the staging table's columns back, casting the text embedding to the
// destination column's vector type.
func (p *pgvectorStore) selectList() string {
	parts := make([]string, len(columns))
	for i, name := range columns {
		parts[i] = name
		if name == "embedding" {
			parts[i] += p.vectorCast()
		}
	}
	return strings.Join(parts, ", ")
}

// DeleteItem removes every view of one item. Deleting nothing is not an error: a tombstone
// for an item that never reached this destination is a normal outcome of a partial run.
func (p *pgvectorStore) DeleteItem(ctx context.Context, datatype, itemID string) error {
	if datatype == "" || itemID == "" {
		return fmt.Errorf("DeleteItem needs a datatype and an item id, got %q and %q", datatype, itemID)
	}
	_, err := p.pool.Exec(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE datatype = $1 AND item_id = $2", p.opts.Table),
		datatype, itemID)
	if err != nil {
		return fmt.Errorf("deleting %s/%s from %s: %w", datatype, itemID, p.opts.Table, err)
	}
	return nil
}

// rowArgs renders one row in column order. It returns the embedding as text and the
// metadata as a JSON string, which is what both write paths send.
func rowArgs(r Row) ([]any, error) {
	vector, err := encodeVector(r.Embedding)
	if err != nil {
		return nil, fmt.Errorf("encoding the embedding of %s/%s view %s: %w",
			r.Datatype, r.ItemID, r.ViewName, err)
	}
	metadata := "{}"
	if len(r.Metadata) > 0 {
		encoded, err := json.Marshal(r.Metadata)
		if err != nil {
			return nil, fmt.Errorf("encoding the metadata of %s/%s view %s: %w",
				r.Datatype, r.ItemID, r.ViewName, err)
		}
		metadata = string(encoded)
	}
	related := r.RelatedKeys
	if related == nil {
		related = []string{} // a NOT NULL text[] column takes an empty array, not NULL
	}
	var fragKey any
	if r.FragKey != "" {
		fragKey = r.FragKey
	}
	return []any{
		r.id(), r.Datatype, r.ItemID, r.ViewName, r.granularity(), fragKey,
		r.Text, vector, r.Model, r.Dims, r.Signature, metadata, related,
	}, nil
}
