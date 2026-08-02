// Unit coverage for row identity, vector encoding and the shared write-path checks. None of
// this needs a database.
package destination

import (
	"math"
	"strings"
	"testing"
)

// testBinding is the binding the check cases are written against.
var testBinding = binding{Model: "Qwen/Qwen3-Embedding-0.6B", Dims: 4, Signature: "sig-1"}

// validRow returns a row that passes every check, for cases to spoil one field of.
func validRow() Row {
	return Row{
		Datatype:  "github/repo",
		ItemID:    "maxwellcudlitz/inget",
		ViewName:  "role",
		Text:      "an ingestion pipeline",
		Embedding: []float32{0.5, 0.5, 0.5, 0.5},
		Model:     testBinding.Model,
		Dims:      testBinding.Dims,
		Signature: testBinding.Signature,
	}
}

func TestRowIDIsInjective(t *testing.T) {
	// The pairs below concatenate to the same string, so a hash without separators would
	// give them one row.
	cases := []struct {
		name string
		a, b [4]string
	}{
		{"datatype and item boundary", [4]string{"a/b", "c", "role", ""}, [4]string{"a", "b/c", "role", ""}},
		{"view and fragment boundary", [4]string{"d", "i", "ro", "le"}, [4]string{"d", "i", "role", ""}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a := RowID(tt.a[0], tt.a[1], tt.a[2], tt.a[3])
			b := RowID(tt.b[0], tt.b[1], tt.b[2], tt.b[3])
			if a == b {
				t.Errorf("RowID%v and RowID%v both = %s", tt.a, tt.b, a)
			}
		})
	}
}

func TestRowIDIsStable(t *testing.T) {
	first := RowID("github/repo", "maxwellcudlitz/inget", "role", "")
	if second := RowID("github/repo", "maxwellcudlitz/inget", "role", ""); first != second {
		t.Errorf("RowID is not deterministic: %s then %s", first, second)
	}
	if len(first) != 64 {
		t.Errorf("RowID length = %d, want 64 hex characters", len(first))
	}
}

func TestRowIDIsDerivedWhenAbsent(t *testing.T) {
	r := validRow()
	want := RowID(r.Datatype, r.ItemID, r.ViewName, r.FragKey)
	if got := r.id(); got != want {
		t.Errorf("id() = %s, want the derived %s", got, want)
	}
	r.ID = "supplied"
	if got := r.id(); got != "supplied" {
		t.Errorf("id() = %s, want the supplied value", got)
	}
}

func TestGranularityDefaultsToItem(t *testing.T) {
	if got := validRow().granularity(); got != GranularityItem {
		t.Errorf("granularity() = %q, want %q", got, GranularityItem)
	}
}

func TestEncodeVector(t *testing.T) {
	cases := []struct {
		name  string
		in    []float32
		want  string
		error bool
	}{
		{name: "single value", in: []float32{1}, want: "[1]"},
		{name: "several values", in: []float32{0.5, -0.25, 0}, want: "[0.5,-0.25,0]"},
		{name: "round-trips float32 precision", in: []float32{0.1}, want: "[0.1]"},
		{name: "empty is rejected", in: nil, error: true},
		{name: "NaN is rejected", in: []float32{float32(math.NaN())}, error: true},
		{name: "infinity is rejected", in: []float32{float32(math.Inf(1))}, error: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encodeVector(tt.in)
			if tt.error {
				if err == nil {
					t.Fatalf("encodeVector(%v) = %q, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("encodeVector(%v): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("encodeVector(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCheckAcceptsAValidBatch(t *testing.T) {
	second := validRow()
	second.ViewName = "surface"
	if err := testBinding.check([]Row{validRow(), second}); err != nil {
		t.Errorf("check: %v", err)
	}
}

func TestCheckRequiresABinding(t *testing.T) {
	err := binding{}.check([]Row{validRow()})
	if err == nil {
		t.Fatal("check with no binding: want an error")
	}
	if !strings.Contains(err.Error(), "AssertModel") {
		t.Errorf("error = %q, want it to name AssertModel", err)
	}
}

func TestCheckRejectsBadRows(t *testing.T) {
	cases := []struct {
		name   string
		spoil  func(*Row)
		expect string
	}{
		{"no datatype", func(r *Row) { r.Datatype = "" }, "datatype"},
		{"no item id", func(r *Row) { r.ItemID = "" }, "item_id"},
		{"no view name", func(r *Row) { r.ViewName = "" }, "view_name"},
		{"no text", func(r *Row) { r.Text = "" }, "text"},
		{"unknown granularity", func(r *Row) { r.Granularity = "chunk" }, "granularity"},
		{"fragment without a key", func(r *Row) { r.Granularity = GranularityFragment }, "frag_key"},
		{"another model", func(r *Row) { r.Model = "other" }, "reindex"},
		{"another signature", func(r *Row) { r.Signature = "sig-2" }, "reindex"},
		{"another dims", func(r *Row) { r.Dims = 8 }, "dims 8"},
		{"short embedding", func(r *Row) { r.Embedding = []float32{1, 2} }, "2 values"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := validRow()
			tt.spoil(&r)
			err := testBinding.check([]Row{r})
			if err == nil {
				t.Fatalf("check(%s): want an error", tt.name)
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Errorf("error = %q, want it to mention %q", err, tt.expect)
			}
		})
	}
}

func TestCheckRejectsARepeatedID(t *testing.T) {
	err := testBinding.check([]Row{validRow(), validRow()})
	if err == nil {
		t.Fatal("check with two rows of the same identity: want an error")
	}
	if !strings.Contains(err.Error(), "repeats an id") {
		t.Errorf("error = %q, want it to name the repeated id", err)
	}
}

func TestFragmentGranularityIsAcceptedWithAKey(t *testing.T) {
	r := validRow()
	r.Granularity, r.FragKey = GranularityFragment, "cmd/inget/main.go"
	if err := testBinding.check([]Row{r}); err != nil {
		t.Errorf("check: %v", err)
	}
}

func TestRowArgsRendersEveryColumn(t *testing.T) {
	r := validRow()
	r.Metadata = map[string]string{"language": "Go"}
	args, err := rowArgs(r)
	if err != nil {
		t.Fatalf("rowArgs: %v", err)
	}
	if len(args) != len(columns) {
		t.Fatalf("rowArgs returned %d values for %d columns", len(args), len(columns))
	}
	// frag_key is NULL for an item-granularity row; related_keys is an empty array rather
	// than NULL, because the column is NOT NULL with an empty-array default.
	if args[5] != nil {
		t.Errorf("frag_key = %v, want nil", args[5])
	}
	if related, ok := args[12].([]string); !ok || related == nil {
		t.Errorf("related_keys = %#v, want an empty non-nil []string", args[12])
	}
	if metadata, ok := args[11].(string); !ok || metadata != `{"language":"Go"}` {
		t.Errorf("metadata = %#v, want the encoded object", args[11])
	}
}
