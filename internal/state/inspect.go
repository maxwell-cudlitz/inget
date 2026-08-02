// Read-only introspection behind `inget state show`: how much of each cascade level is
// persisted, and how the recent runs ended.
//
// This is the one place a timestamp crosses the Store boundary, which the package comment
// said would wait until something needed it. Nothing else does: the guards are compared for
// inequality and the run history is ordered by ULID, so `started_at` is read here for a human
// and nowhere for a decision. The two drivers store it differently — PostgreSQL as
// timestamptz, SQLite as CURRENT_TIMESTAMP's UTC text — so the value is scanned untyped and
// normalized by asTime rather than by a second set of statements.
package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	countItemsSQL       = `SELECT count(*) FROM items WHERE datatype = ? AND deleted_at IS NULL`
	countTombstonedSQL  = `SELECT count(*) FROM items WHERE datatype = ? AND deleted_at IS NOT NULL`
	countFragmentsSQL   = `SELECT count(*) FROM fragments WHERE datatype = ?`
	countMissingSQL     = `SELECT count(*) FROM fragments WHERE datatype = ? AND missing_runs > 0`
	countDerivationsSQL = `SELECT count(*) FROM derivations WHERE datatype = ?`
	countViewsSQL       = `SELECT count(*) FROM views WHERE datatype = ?`
	countEmbeddedSQL    = `SELECT count(*) FROM views WHERE datatype = ? AND embedded_hash IS NOT NULL`
	countRefsSQL        = `SELECT count(*) FROM refs WHERE from_datatype = ?`
	countStaleRefsSQL   = `SELECT count(*) FROM refs WHERE from_datatype = ? AND stale_depth IS NOT NULL`

	runsColumns = `run_id, "binary", datatype, scope, status, config_hash, stats, started_at, finished_at`

	// Ordering by run_id is ordering by start time, because run IDs are ULIDs.
	runsRecentSQL = `SELECT ` + runsColumns + ` FROM runs ORDER BY run_id DESC LIMIT ?`

	runsRecentByDatatypeSQL = `SELECT ` + runsColumns + `
FROM runs WHERE COALESCE(datatype, '') = ? ORDER BY run_id DESC LIMIT ?`
)

// Counts is how much of one datatype each cascade level holds. Every figure is a row count,
// so a level that has never run reads as zero rather than as absent.
type Counts struct {
	Items            int `json:"items"`
	Tombstoned       int `json:"tombstoned"`
	Fragments        int `json:"fragments"`
	MissingFragments int `json:"missing_fragments"`
	Derivations      int `json:"derivations"`
	Views            int `json:"views"`
	EmbeddedViews    int `json:"embedded_views"`
	Refs             int `json:"refs"`
	StaleRefs        int `json:"stale_refs"`
}

// RunRecord is one row of the run history. Stats is carried as the raw JSON the run wrote,
// because its shape belongs to whichever binary produced it.
type RunRecord struct {
	ID         string    `json:"run_id"`
	Binary     string    `json:"binary"`
	Datatype   string    `json:"datatype,omitempty"`
	Scope      string    `json:"scope"`
	Status     string    `json:"status"`
	ConfigHash string    `json:"config_hash"`
	Stats      string    `json:"stats"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

// Counts implements Store.
func (s *store) Counts(ctx context.Context, datatype string) (Counts, error) {
	var c Counts
	for _, q := range []struct {
		into  *int
		query string
	}{
		{&c.Items, countItemsSQL},
		{&c.Tombstoned, countTombstonedSQL},
		{&c.Fragments, countFragmentsSQL},
		{&c.MissingFragments, countMissingSQL},
		{&c.Derivations, countDerivationsSQL},
		{&c.Views, countViewsSQL},
		{&c.EmbeddedViews, countEmbeddedSQL},
		{&c.Refs, countRefsSQL},
		{&c.StaleRefs, countStaleRefsSQL},
	} {
		if _, err := s.get(ctx, q.query, []any{datatype}, q.into); err != nil {
			return Counts{}, fmt.Errorf("counting %s state: %w", datatype, err)
		}
	}
	return c, nil
}

// RecentRuns implements Store.
func (s *store) RecentRuns(ctx context.Context, datatype string, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("reading run history: limit %d, want 1 or more", limit)
	}
	query, args := runsRecentSQL, []any{limit}
	if datatype != "" {
		query, args = runsRecentByDatatypeSQL, []any{datatype, limit}
	}

	var runs []RunRecord
	err := s.each(ctx, query, args, func(rows *sql.Rows) error {
		r, err := scanRun(rows)
		if err != nil {
			return err
		}
		runs = append(runs, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading run history: %w", err)
	}
	return runs, nil
}

// scanRun reads one run row, normalizing the two nullable columns and both timestamps.
func scanRun(rows *sql.Rows) (RunRecord, error) {
	var (
		r          RunRecord
		datatype   sql.NullString
		started    any
		finished   any
		parseError error
	)
	if err := rows.Scan(&r.ID, &r.Binary, &datatype, &r.Scope, &r.Status,
		&r.ConfigHash, &r.Stats, &started, &finished); err != nil {
		return RunRecord{}, fmt.Errorf("scanning run: %w", err)
	}
	r.Datatype = datatype.String
	if r.StartedAt, parseError = asTime(started); parseError != nil {
		return RunRecord{}, fmt.Errorf("run %s started_at: %w", r.ID, parseError)
	}
	if r.FinishedAt, parseError = asTime(finished); parseError != nil {
		return RunRecord{}, fmt.Errorf("run %s finished_at: %w", r.ID, parseError)
	}
	return r, nil
}

// sqliteTimestamp is what CURRENT_TIMESTAMP renders on SQLite: UTC, space-separated, no zone.
const sqliteTimestamp = "2006-01-02 15:04:05"

// asTime normalizes a scanned timestamp. A NULL becomes the zero time, which is what an
// unfinished run has for finished_at.
func asTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return t, nil
	case string:
		return parseTimestamp(t)
	case []byte:
		return parseTimestamp(string(t))
	default:
		return time.Time{}, fmt.Errorf("cannot read a timestamp from %T", v)
	}
}

// parseTimestamp reads SQLite's rendering.
func parseTimestamp(text string) (time.Time, error) {
	parsed, err := time.ParseInLocation(sqliteTimestamp, text, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing timestamp %q: %w", text, err)
	}
	return parsed, nil
}
