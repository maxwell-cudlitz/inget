// Everything that differs between PostgreSQL and SQLite, in one place.
//
// The statements in this package are written once, against the intersection of the two
// dialects: both support upserts with ON CONFLICT and excluded, RETURNING, row-value IN,
// and CURRENT_TIMESTAMP. Exactly three differences remain, and they are the fields below.
// If a fourth appears, it belongs here rather than in a second implementation of the
// twenty-odd statements the Store needs.
package state

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3/database"
)

// schemaName is the PostgreSQL schema every table lives in. SQLite has no schemas, so its
// tables are bare and the connection reaches them without qualification either way.
const schemaName = "inget_state"

// dialect describes one driver's deviations from the shared SQL.
type dialect struct {
	name       string           // config vocabulary: postgres | sqlite
	goose      database.Dialect // migration dialect
	ordinal    bool             // true when placeholders are $1..$N rather than ?
	skipLocked string           // row-locking clause for work claiming, empty when absent
	advisory   bool             // true when Lock uses a session advisory lock
}

var dialects = map[string]dialect{
	DriverPostgres: {
		name:  DriverPostgres,
		goose: database.DialectPostgres,
		// pgx speaks the extended protocol, which takes ordinal placeholders only.
		ordinal: true,
		// Without SKIP LOCKED a second worker blocks on the first worker's claim
		// instead of taking the next item.
		skipLocked: " FOR UPDATE SKIP LOCKED",
		// A session advisory lock disappears when the connection does, so a killed pod
		// releases it without anything having to notice.
		advisory: true,
	},
	DriverSQLite: {
		name:  DriverSQLite,
		goose: database.DialectSQLite3,
		// SQLite serializes writers, so claiming needs no locking clause: the UPDATE
		// that claims work holds the write lock for its duration.
		skipLocked: "",
		advisory:   false,
	},
}

// lookupDialect resolves a configured driver name.
func lookupDialect(driver string) (dialect, error) {
	d, ok := dialects[driver]
	if !ok {
		return dialect{}, fmt.Errorf("unknown state driver %q (want %s or %s)", driver, DriverPostgres, DriverSQLite)
	}
	return d, nil
}

// rebind rewrites ? placeholders as $1..$N for dialects that require ordinals, and returns
// the statement untouched otherwise.
//
// Statements are constants, so this could be precomputed; at one string scan per query
// against one network round trip it is not worth the cache. Single-quoted literals are
// skipped so that a '?' inside one would survive — no statement here has one, but the
// rewriter should not be the reason that stays true.
func (d dialect) rebind(query string) string {
	if !d.ordinal || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n, inLiteral := 0, false
	for i := 0; i < len(query); i++ {
		switch c := query[i]; {
		case c == '\'':
			inLiteral = !inLiteral // a doubled '' toggles twice, which is a no-op
			b.WriteByte(c)
		case c == '?' && !inLiteral:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
