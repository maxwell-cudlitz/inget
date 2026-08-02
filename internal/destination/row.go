// Row identity, vector encoding, and the checks every write path shares.
//
// Vectors cross the wire in pgvector's text format and are cast to the column's type
// server-side. The binary format would be smaller, but halfvec is IEEE 754 half precision
// and hand-rolling float32 to float16 conversion — subnormals, overflow, round-to-nearest-
// even — is a way to store vectors that are quietly slightly wrong. The server's own parser
// gets that right, and a wrong vector is invisible: nothing fails, search answers just get
// worse. The cost is roughly four times the bytes on the wire during a cold load, which is
// documented in AGENTS.md rather than traded for that risk.
package destination

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RowID derives the primary key of a row from its identity.
//
// The parts are NUL-separated because none of them can contain a NUL byte and plain
// concatenation would not be injective: datatype "a/b" with view "c" and datatype "a" with
// view "b/c" would otherwise hash to the same row. frag_key is empty for item-granularity
// rows, which is a distinct input from any fragment key.
func RowID(datatype, itemID, viewName, fragKey string) string {
	h := sha256.New()
	for _, part := range []string{datatype, itemID, viewName, fragKey} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// granularity reads the row's granularity, defaulting to item.
func (r Row) granularity() string {
	if r.Granularity == "" {
		return GranularityItem
	}
	return r.Granularity
}

// id returns the row's key, deriving it when the caller left it empty so that a row is
// addressable whether or not the caller bothered.
func (r Row) id() string {
	if r.ID != "" {
		return r.ID
	}
	return RowID(r.Datatype, r.ItemID, r.ViewName, r.FragKey)
}

// binding is the (model, dims, signature) triple a destination table is bound to (D7).
type binding struct {
	Model     string
	Dims      int
	Signature string
}

// bound reports whether a binding has been established.
func (b binding) bound() bool { return b.Model != "" }

// check validates a batch against the binding and against itself. It is the one gate both
// write paths pass through, so the two cannot disagree about what a valid row is.
//
// Duplicate IDs inside one call are rejected rather than deduplicated. An ID is derived
// from identity, not content, so two rows sharing one is a caller that computed the same
// view twice — and both PostgreSQL upsert forms refuse to touch a row twice in one
// statement anyway, so the alternative is a confusing error from the server.
func (b binding) check(rows []Row) error {
	if !b.bound() {
		return fmt.Errorf("destination is not bound to an embedder: call AssertModel before writing")
	}
	seen := make(map[string]struct{}, len(rows))
	for i, r := range rows {
		if err := b.checkRow(r); err != nil {
			return fmt.Errorf("row %d (%s/%s view %s): %w", i, r.Datatype, r.ItemID, r.ViewName, err)
		}
		id := r.id()
		if _, dup := seen[id]; dup {
			return fmt.Errorf("row %d (%s/%s view %s) repeats an id already in this batch",
				i, r.Datatype, r.ItemID, r.ViewName)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// checkRow validates one row's required fields, granularity and provenance.
func (b binding) checkRow(r Row) error {
	switch {
	case r.Datatype == "":
		return fmt.Errorf("datatype is required")
	case r.ItemID == "":
		return fmt.Errorf("item_id is required")
	case r.ViewName == "":
		return fmt.Errorf("view_name is required")
	case r.Text == "":
		return fmt.Errorf("text is required: an empty view should not have been embedded")
	}
	switch r.granularity() {
	case GranularityItem:
	case GranularityFragment:
		if r.FragKey == "" {
			return fmt.Errorf("granularity %q requires a frag_key", GranularityFragment)
		}
	default:
		return fmt.Errorf("granularity %q is not %s or %s", r.Granularity, GranularityItem, GranularityFragment)
	}
	// D7 at the write boundary. The registry already refused a mismatched AssertModel; this
	// catches a batch assembled from two embedders inside one process.
	if r.Model != b.Model || r.Signature != b.Signature {
		return fmt.Errorf("model %q signature %q disagrees with the destination binding (model %q signature %q); "+
			"run `inget reindex` to rebind the table",
			r.Model, r.Signature, b.Model, b.Signature)
	}
	if r.Dims != b.Dims {
		return fmt.Errorf("dims %d disagrees with the destination binding (%d)", r.Dims, b.Dims)
	}
	if len(r.Embedding) != b.Dims {
		return fmt.Errorf("embedding has %d values, want %d", len(r.Embedding), b.Dims)
	}
	return nil
}

// encodeVector renders a vector in pgvector's text input format.
//
// Values are formatted with the shortest representation that round-trips as a float32,
// which is exact for the vector storage type and more precision than halfvec keeps. A
// non-finite value is refused here rather than by the server: pgvector rejects it too, but
// only after a round trip and without naming the position.
func encodeVector(v []float32) (string, error) {
	if len(v) == 0 {
		return "", fmt.Errorf("vector is empty")
	}
	var b strings.Builder
	b.Grow(len(v) * 12)
	b.WriteByte('[')
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return "", fmt.Errorf("vector value at index %d is %v, which pgvector cannot store", i, f)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String(), nil
}
