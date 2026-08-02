// Driver registration.
//
// modernc.org/sqlite registers itself under the name "sqlite" as a side effect of being
// imported, which is the only reason this import exists. It is a pure-Go translation of
// SQLite, so it keeps binaries statically cross-compilable, and it costs roughly 6 MB of
// stripped binary. PostgreSQL needs no registration here: openPostgres builds a pgxpool
// and hands database/sql a connector directly.
package state

import _ "modernc.org/sqlite"

// sqliteDriverName is the name modernc.org/sqlite registers with database/sql.
const sqliteDriverName = "sqlite"
