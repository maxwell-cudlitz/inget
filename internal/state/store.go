// The execution helpers every statement in this package goes through.
//
// They exist for three reasons: placeholders are rebound in exactly one place, result sets
// are always closed and always have their iteration error checked, and a transaction is
// always either committed or rolled back. Each of those is a mistake that is easy to make
// once per query and impossible to make once per package.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// exec runs a statement and reports how many rows it affected.
func (s *store) exec(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := s.db.ExecContext(ctx, s.d.rebind(query), args...)
	if err != nil {
		return 0, fmt.Errorf("executing statement: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting affected rows: %w", err)
	}
	return affected, nil
}

// get scans a single-row query into dest, reporting false when there is no row. A missing
// row is not an error: for every caller here, absence is a normal state.
func (s *store) get(ctx context.Context, query string, args []any, dest ...any) (bool, error) {
	err := s.db.QueryRowContext(ctx, s.d.rebind(query), args...).Scan(dest...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("querying row: %w", err)
	}
	return true, nil
}

// each runs a query and calls scan once per row. Scan errors are returned as scan produced
// them, since the callback knows what it was reading and this does not.
func (s *store) each(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, s.d.rebind(query), args...)
	if err != nil {
		return fmt.Errorf("querying rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating rows: %w", err)
	}
	return nil
}

// tx is a transaction that rebinds placeholders the way the store does.
type tx struct {
	*sql.Tx
	d dialect
}

// exec runs one statement inside the transaction.
func (t tx) exec(ctx context.Context, query string, args ...any) error {
	if _, err := t.ExecContext(ctx, t.d.rebind(query), args...); err != nil {
		return fmt.Errorf("executing statement: %w", err)
	}
	return nil
}

// execMany prepares one statement and runs it once per argument row, which is how every
// batch write here reaches the database: one round trip to prepare, one per row, all
// committed together or not at all.
func (t tx) execMany(ctx context.Context, query string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := t.PrepareContext(ctx, t.d.rebind(query))
	if err != nil {
		return fmt.Errorf("preparing statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, args := range rows {
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return fmt.Errorf("executing prepared statement: %w", err)
		}
	}
	return nil
}

// inTx runs fn in a transaction, rolling back on any error and on panic.
func (s *store) inTx(ctx context.Context, fn func(tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()

	if err := fn(tx{Tx: sqlTx, d: s.d}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	committed = true
	return nil
}

// nullText renders an empty string as SQL NULL, so "not recorded" is one value in the
// database rather than two.
func nullText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullNumber renders a zero as SQL NULL, used for the columns where zero is not a
// meaningful value (a vector has no zero dimension count).
func nullNumber(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// marshalJSON renders a value for a JSON column. A nil value becomes an empty object so
// the column's NOT NULL default is never contradicted.
func marshalJSON(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encoding JSON column: %w", err)
	}
	return string(encoded), nil
}
