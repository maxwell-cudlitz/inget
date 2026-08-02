// The read path: filtered nearest-neighbour search over the single shared HNSW index.
//
// The two SET LOCAL statements are what make D9 work. Before pgvector 0.8.0 an HNSW scan
// returned ef_search candidates and only then applied the WHERE clause, so a search
// restricted to one view out of eight kept about an eighth of them and recall collapsed.
// Iterative scans rescan the graph until enough rows pass the filter, which is why one
// index over every view is enough and no view needs its own DDL.
//
// SET LOCAL scopes both settings to the transaction, so a pooled connection is never handed
// back with them still applied.
package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Search returns the Limit nearest neighbours of q.Embedding, optionally restricted to one
// view of one datatype.
func (p *pgvectorStore) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if q.Datatype == "" {
		return nil, fmt.Errorf("Search needs a datatype")
	}
	if q.Limit <= 0 {
		return nil, fmt.Errorf("Search needs a positive limit, got %d", q.Limit)
	}
	vector, err := encodeVector(q.Embedding)
	if err != nil {
		return nil, fmt.Errorf("encoding the query vector: %w", err)
	}
	efSearch := q.EFSearch
	if efSearch <= 0 {
		efSearch = p.opts.EFSearch
	}

	var results []SearchResult
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		// Neither setting can be a bind parameter. ef_search is an integer rendered by
		// strconv, so there is nothing here for a caller to inject.
		if _, err := tx.Exec(ctx, "SET LOCAL hnsw.iterative_scan = relaxed_order"); err != nil {
			return fmt.Errorf("enabling iterative index scans: %w", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL hnsw.ef_search = "+strconv.Itoa(efSearch)); err != nil {
			return fmt.Errorf("setting hnsw.ef_search to %d: %w", efSearch, err)
		}
		results, err = p.query(ctx, tx, vector, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// query runs the documented search statement and scans it.
func (p *pgvectorStore) query(ctx context.Context, tx pgx.Tx, vector string, q SearchQuery) ([]SearchResult, error) {
	cast := p.vectorCast()
	statement := fmt.Sprintf(`SELECT id, item_id, view_name, text, metadata,
			1 - (embedding <=> $1%s) AS score
		FROM %s
		WHERE datatype = $2 AND ($3::text IS NULL OR view_name = $3)
		ORDER BY embedding <=> $1%s
		LIMIT $4`, cast, p.opts.Table, cast)

	var view any
	if q.ViewName != "" {
		view = q.ViewName
	}
	rows, err := tx.Query(ctx, statement, vector, q.Datatype, view, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("searching %s: %w", p.opts.Table, err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, q.Limit)
	for rows.Next() {
		var r SearchResult
		var metadata []byte
		if err := rows.Scan(&r.ID, &r.ItemID, &r.ViewName, &r.Text, &metadata, &r.Score); err != nil {
			return nil, fmt.Errorf("scanning a search result from %s: %w", p.opts.Table, err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &r.Metadata); err != nil {
				return nil, fmt.Errorf("decoding the metadata of %s: %w", r.ID, err)
			}
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating search results from %s: %w", p.opts.Table, err)
	}
	return results, nil
}
