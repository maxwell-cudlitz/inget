// Query-only validation: establish that the configured table is safe to search without
// creating a registry row, migrating schema or granting this handle permission to write.
package destination

import (
	"context"
	"errors"
	"fmt"
)

// ValidateModel checks the existing model binding and every read-side prerequisite.
// It intentionally leaves p.model untouched: successful validation is not AssertModel,
// and must not make an otherwise unbound writer handle accept rows.
func (p *pgvectorStore) ValidateModel(ctx context.Context, model string, dims int, signature string) error {
	switch {
	case model == "":
		return errors.New("ValidateModel needs a model name")
	case signature == "":
		return errors.New("ValidateModel needs an embedder signature")
	case dims != p.opts.Dims:
		return fmt.Errorf("embedder yields %d dimensions but destination %s is configured for %d; "+
			"align models.embedder with the destination", dims, p.opts.Table, p.opts.Dims)
	}
	if err := p.checkExtension(ctx); err != nil {
		return err
	}
	if err := p.checkQueryShape(ctx); err != nil {
		return err
	}
	have, found, err := p.readBinding(ctx)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("destination %s has no model binding; have an authorized writer run "+
			"`inget migrate` and initialize it with ingestion or `inget reindex` before querying; "+
			"query validation never creates registry rows", p.opts.Table)
	}
	if have.Model != model || have.Dims != dims || have.Signature != signature {
		return fmt.Errorf("destination %s is bound to model %q (%d dims, signature %q) but this query uses "+
			"model %q (%d dims, signature %q); align the query configuration or have an authorized writer "+
			"run `inget reindex` rather than mixing two vector spaces",
			p.opts.Table, have.Model, have.Dims, have.Signature, model, dims, signature)
	}
	return p.checkQueryRead(ctx)
}

// checkQueryShape inspects the actual column rather than trusting the registry or
// configuration: those can agree while pointing at a table of another type or width.
func (p *pgvectorStore) checkQueryShape(ctx context.Context) error {
	var installed string
	err := p.pool.QueryRow(ctx, `
		SELECT format_type(atttypid, atttypmod)
		FROM pg_attribute
		WHERE attrelid = $1::regclass AND attname = 'embedding' AND NOT attisdropped`,
		p.opts.Table).Scan(&installed)
	if err != nil {
		return fmt.Errorf("reading the embedding column of destination %s: %w", p.opts.Table, err)
	}
	want := fmt.Sprintf("%s(%d)", p.opts.Storage, p.opts.Dims)
	if installed != want {
		return fmt.Errorf("destination %s embedding column is %s but configuration requires %s; "+
			"align the query configuration or migrate a destination with the required shape",
			p.opts.Table, installed, want)
	}
	return nil
}

// checkQueryRead asks PostgreSQL to resolve and authorize every column Search reads,
// without scanning corpus rows or needing a synthetic embedding/model request.
func (p *pgvectorStore) checkQueryRead(ctx context.Context) error {
	rows, err := p.pool.Query(ctx, `SELECT id, item_id, view_name, text, metadata, embedding, datatype
		FROM `+p.opts.Table+` LIMIT 0`)
	if err != nil {
		return fmt.Errorf("checking SELECT access to destination %s: %w", p.opts.Table, err)
	}
	defer rows.Close()
	for rows.Next() {
		// LIMIT 0 returns no rows; fully draining also releases a one-connection pool.
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("checking SELECT access to destination %s: %w", p.opts.Table, err)
	}
	return nil
}
