// Rebinding and pruning: the two operations `inget reindex` needs and nothing else may use.
//
// AssertModel is deliberately unable to change a binding, because for every other caller a
// disagreement means someone edited models.embedder without meaning to rewrite an index. That
// leaves exactly one legitimate way forward when the change was intended, and this is it:
// replace the binding, re-embed everything state holds, then delete whatever the new model
// never wrote. The three steps are separate calls because the middle one is long, resumable,
// and lives in internal/reindex.
//
// Between the rebind and the end of the pass, the table holds two vector spaces. That window is
// unavoidable — the write path cannot accept new vectors before the binding moves — and it is
// why RebindModel logs at warn level rather than returning quietly.
package destination

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// RebindModel replaces the destination's model binding, reporting whether it changed.
func (p *pgvectorStore) RebindModel(ctx context.Context, model string, dims int, signature string) (bool, error) {
	if err := p.checkBindable(model, dims, signature); err != nil {
		return false, err
	}
	want := binding{Model: model, Dims: dims, Signature: signature}

	have, found, err := p.readBinding(ctx)
	if err != nil {
		return false, err
	}
	if found && have == want {
		p.mu.Lock()
		p.model = have
		p.mu.Unlock()
		return false, nil
	}

	if _, err := p.pool.Exec(ctx,
		`INSERT INTO `+registryTable+` (table_name, model, dims, signature)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (table_name) DO UPDATE SET
		     model     = EXCLUDED.model,
		     dims      = EXCLUDED.dims,
		     signature = EXCLUDED.signature,
		     bound_at  = now()`,
		p.opts.Table, model, dims, signature); err != nil {
		return false, fmt.Errorf("rebinding destination %s to model %s: %w", p.opts.Table, model, err)
	}

	p.mu.Lock()
	p.model = want
	p.mu.Unlock()

	if found {
		slog.WarnContext(ctx, "destination rebound to a new embedder",
			"table", p.opts.Table, "previous_model", have.Model, "previous_signature", have.Signature,
			"model", model, "dims", dims,
			"note", "rows written by the previous model are stale until the reindex pass rewrites them")
	}
	return true, nil
}

// PruneStaleVectors deletes rows that disagree with the current binding.
func (p *pgvectorStore) PruneStaleVectors(ctx context.Context, datatype string) (int, error) {
	bound := p.binding()
	if !bound.bound() {
		return 0, errors.New("destination is not bound to an embedder: call AssertModel or RebindModel first")
	}

	statement := fmt.Sprintf(
		`DELETE FROM %s WHERE (model <> $1 OR signature <> $2 OR dims <> $3)`, p.opts.Table)
	args := []any{bound.Model, bound.Signature, bound.Dims}
	if datatype != "" {
		statement += ` AND datatype = $4`
		args = append(args, datatype)
	}

	tag, err := p.pool.Exec(ctx, statement, args...)
	if err != nil {
		return 0, fmt.Errorf("pruning stale vectors from %s: %w", p.opts.Table, err)
	}
	deleted := int(tag.RowsAffected())
	if deleted > 0 {
		slog.InfoContext(ctx, "pruned vectors from a previous embedder",
			"table", p.opts.Table, "datatype", datatype, "deleted", deleted,
			"model", bound.Model, "dims", bound.Dims)
	}
	return deleted, nil
}

// checkBindable rejects a binding that cannot be recorded, with the same rules AssertModel
// applies: a width the table was not migrated for is a configuration error, not something a
// rebind can fix, because the column type would refuse the vectors.
func (p *pgvectorStore) checkBindable(model string, dims int, signature string) error {
	switch {
	case model == "":
		return errors.New("rebinding needs a model name")
	case signature == "":
		return errors.New("rebinding needs an embedder signature")
	case dims != p.opts.Dims:
		return fmt.Errorf("embedder yields %d dimensions but destination %s holds %s(%d); "+
			"a rebind cannot widen the column, so migrate a destination of the right width instead",
			dims, p.opts.Table, p.opts.Storage, p.opts.Dims)
	}
	return nil
}

// readBinding returns the recorded binding and whether there is one.
func (p *pgvectorStore) readBinding(ctx context.Context) (binding, bool, error) {
	var have binding
	err := p.pool.QueryRow(ctx,
		`SELECT model, dims, signature FROM `+registryTable+` WHERE table_name = $1`,
		p.opts.Table).Scan(&have.Model, &have.Dims, &have.Signature)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return binding{}, false, nil
		}
		return binding{}, false, fmt.Errorf("reading the model binding of destination %s: %w",
			p.opts.Table, err)
	}
	return have, true, nil
}
