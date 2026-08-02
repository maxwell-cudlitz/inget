// The value types crossing the Store boundary, one per table it is worth naming.
//
// They mirror the columns of docs/feature-design.md's state schema rather than the shapes
// a connector or an enricher happens to hold, which is why an empty string means "not
// recorded yet" throughout: a nullable column reads back as "" and writes back as NULL, so
// callers never handle a null and the database never stores an empty string that means
// something different from absent.
package state

// Driver names, matching the state.driver values internal/config accepts.
const (
	DriverPostgres = "postgres"
	DriverSQLite   = "sqlite"
)

// Run scopes. A partial run touches named items only and issues no tombstones.
const (
	ScopeFull    = "full"
	ScopePartial = "partial"
)

// Run statuses. Running and Interrupted are the resumable ones.
const (
	RunRunning     = "running"
	RunOK          = "ok"
	RunFailed      = "failed"
	RunInterrupted = "interrupted"
)

// Work item statuses.
const (
	WorkPending = "pending"
	WorkClaimed = "claimed"
	WorkDone    = "done"
	WorkFailed  = "failed"
)

// Item is one record's identity and level-0 change token.
//
// Source and RunID are not part of the design document's Item, which describes what a
// connector produces; the items table records both, so the persisted shape carries them.
// ComposedHash is likewise here rather than a trailing parameter, so PutItem takes one
// value that either matches the row or does not.
type Item struct {
	ID           string            // stable within the source
	Source       string            // config source name
	Fingerprint  string            // level-0 token
	ComposedHash string            // level-2 input, unscoped; "" before composition
	Metadata     map[string]string // serialized as JSON
	RunID        string            // run that last observed the item
}

// FragmentState is one persisted fragment: its level-1 token plus where its content went.
type FragmentState struct {
	Key         string
	Fingerprint string // level-1 token
	BlobRef     string // sha256 in the blob store; "" when truncated or empty
	SizeBytes   int64
	Tier        int
	MissingRuns int // runs this fragment has been absent from
}

// Derivation is one cached per-fragment enrichment, keyed by fragment fingerprint and
// enricher signature so that either changing is a miss (D2).
type Derivation struct {
	CacheKey  string
	Datatype  string
	ItemID    string
	FragKey   string
	Signature string
	Output    string
}

// ViewState is one generated view of one item, carrying the level-2 and level-3 guards.
//
// Text is the text the stored vector was produced from, not simply the last text
// generated. The two differ whenever a regeneration drifted less than drift_threshold and
// so was not re-embedded (D4): keeping the embedded text here makes the next drift
// comparison measure distance from the vector rather than from the previous comparison,
// which is what stops a sequence of sub-threshold changes from walking the text
// arbitrarily far away from what was actually indexed.
type ViewState struct {
	Name         string
	InputHash    string // level-2 guard, scoped to the view's dependency globs
	Text         string // the text behind EmbeddedHash's vector
	EmbeddedHash string // level-3 guard; "" until embedded
	Model        string // embedder that produced the vector
	Dims         int
	Signature    string
}

// Checkpoint is everything one item's successful processing changes in state: its level-0
// token, its level-1 tokens, the guards of every view it touched, and the work row that
// records it as done.
//
// It is one type because it is one transaction (design stage 10). Writing any part of it
// before the destination upsert it attests to is how an item that failed halfway reports
// itself as up to date: the next run reconciles it as unchanged and nothing regenerates
// it until some unrelated fragment happens to change.
type Checkpoint struct {
	RunID     string
	Item      Item
	Fragments []FragmentState
	Views     []ViewState
}

// ItemKey identifies one item across datatypes. It is both the source of a reference edge
// and what a reverse lookup returns, so there is one type rather than two identical ones.
type ItemKey struct {
	Datatype string
	ItemID   string
}

// RefEdge is one resolved reference from an item to something else.
type RefEdge struct {
	Name        string // reference name from config
	Kind        string // resolver kind: inget, http
	Key         string // resolved key in the referent's namespace
	Fingerprint string // referent's change token; "" when the resolver cannot supply one
}

// Run is the record of one process execution. Status and timestamps are managed by the
// store, so they are not fields here.
type Run struct {
	ID         string // ULID
	Binary     string // inget | inget-fetch
	Datatype   string // "" when the run spans every configured datatype
	Scope      string // full | partial
	ConfigHash string
}
