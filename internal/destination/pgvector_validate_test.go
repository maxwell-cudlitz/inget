// Read-only model validation must work with the privileges used by a serving process,
// without binding an uninitialized table or turning validation into write authorization.
package destination

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestValidateModelRejectsInvalidInputsBeforeConnecting(t *testing.T) {
	p := &pgvectorStore{opts: testOptions("not-used")}
	for _, tt := range []struct {
		name, model, signature, want string
		dims                         int
	}{
		{"model", "", testSignature, "model name", testDims},
		{"signature", testModel, "", "signature", testDims},
		{"dimensions", testModel, testSignature, "configured for", testDims * 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := p.ValidateModel(t.Context(), tt.model, tt.dims, tt.signature)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateModel error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateModelUsesOnlyReads(t *testing.T) {
	owner := newStore(t)
	writer, reader := restrictedQueryStores(t, owner)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var readOnly string
	if err := reader.pool.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil {
		t.Fatal(err)
	}
	if readOnly != "on" || reader.pool.Config().MaxConns != 1 {
		t.Fatalf("reader defaults: read_only=%s max_conns=%d", readOnly, reader.pool.Config().MaxConns)
	}
	if err := reader.ValidateModel(ctx, testModel, testDims, testSignature); err != nil {
		t.Fatalf("ValidateModel as SELECT-only reader: %v", err)
	}
	q := SearchQuery{Embedding: unitVector(1), Datatype: testDatatype, Limit: 5}
	if hits, err := reader.Search(ctx, q); err != nil || len(hits) != 0 {
		t.Fatalf("empty search = %v, %v", hits, err)
	}
	if err := writer.AssertModel(ctx, testModel, testDims, testSignature); err != nil {
		t.Fatalf("AssertModel as writer: %v", err)
	}
	row := testRow("acme/readable", "role", unitVector(1))
	if err := writer.Upsert(ctx, []Row{row}); err != nil {
		t.Fatalf("Upsert as writer: %v", err)
	}
	if hits, err := reader.Search(ctx, q); err != nil || len(hits) != 1 {
		t.Fatalf("seeded search = %v, %v", hits, err)
	}
	if err := reader.Upsert(ctx, []Row{row}); err == nil || !strings.Contains(err.Error(), "AssertModel") {
		t.Fatalf("validation authorized writing: %v", err)
	}
	if err := reader.AssertModel(ctx, testModel, testDims, testSignature); err == nil {
		t.Fatal("AssertModel as read-only reader unexpectedly succeeded")
	}
	// Disable the read-only session default to prove grants deny writes independently.
	if _, err := reader.pool.Exec(ctx, "SET default_transaction_read_only = off"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.pool.Exec(ctx, "DELETE FROM "+testTable); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("reader DELETE = %v, want insufficient_privilege", err)
	}
	if err := reader.AssertModel(ctx, testModel, testDims, testSignature); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("reader registry INSERT = %v, want insufficient_privilege", err)
	}
}

func TestValidateModelRefusesMissingBindingWithoutCreatingIt(t *testing.T) {
	p := newStore(t)
	_, reader := restrictedQueryStores(t, p)
	if _, err := p.pool.Exec(t.Context(), "DELETE FROM "+registryTable+" WHERE table_name=$1", testTable); err != nil {
		t.Fatal(err)
	}
	err := reader.ValidateModel(t.Context(), testModel, testDims, testSignature)
	if err == nil || !strings.Contains(err.Error(), "no model binding") || !strings.Contains(err.Error(), "authorized writer") {
		t.Fatalf("missing binding error = %v", err)
	}
	if _, found, err := p.readBinding(t.Context()); err != nil || found {
		t.Fatalf("validation created binding: found=%v err=%v", found, err)
	}
}

func TestValidateModelRejectsMismatchedBinding(t *testing.T) {
	for _, column := range []string{"model", "dims", "signature"} {
		t.Run(column, func(t *testing.T) {
			p := newStore(t)
			_, reader := restrictedQueryStores(t, p)
			value := any("other")
			if column == "dims" {
				value = testDims * 2
			}
			if _, err := p.pool.Exec(t.Context(), "UPDATE "+registryTable+" SET "+column+"=$1 WHERE table_name=$2", value, testTable); err != nil {
				t.Fatal(err)
			}
			err := reader.ValidateModel(t.Context(), testModel, testDims, testSignature)
			if err == nil || !strings.Contains(err.Error(), "inget reindex") {
				t.Fatalf("mismatch error = %v", err)
			}
		})
	}
}

func TestValidateModelChecksPhysicalShapeAndReadAccess(t *testing.T) {
	for _, tt := range []struct{ name, change, want string }{
		{"width", "ALTER TABLE " + testTable + " ALTER COLUMN embedding TYPE halfvec(8)", "halfvec(8)"},
		{"storage", "ALTER TABLE " + testTable + " ALTER COLUMN embedding TYPE vector(4)", "vector(4)"},
		{"missing column", "ALTER TABLE " + testTable + " DROP COLUMN metadata", "metadata"},
		{"missing table", "DROP TABLE " + testTable, testTable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newStore(t)
			_, reader := restrictedQueryStores(t, p)
			if _, err := p.pool.Exec(t.Context(), "DROP INDEX "+testTable+"_hnsw_idx"); err != nil {
				t.Fatal(err)
			}
			if _, err := p.pool.Exec(t.Context(), tt.change); err != nil {
				t.Fatal(err)
			}
			err := reader.ValidateModel(t.Context(), testModel, testDims, testSignature)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateModel after %s = %v, want %q", tt.change, err, tt.want)
			}
		})
	}
}

func TestValidateModelRequiresAllQueryReads(t *testing.T) {
	for _, tt := range []struct{ name, revoke, grant string }{
		{"registry", registryTable, ""},
		{"destination", testTable, ""},
		{"embedding", testTable, "id, item_id, view_name, text, metadata, datatype"},
		{"metadata", testTable, "id, item_id, view_name, text, embedding, datatype"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newStore(t)
			_, reader := restrictedQueryStores(t, p)
			name := pgx.Identifier{reader.pool.Config().ConnConfig.User}.Sanitize()
			if _, err := p.pool.Exec(t.Context(), "REVOKE SELECT ON "+tt.revoke+" FROM "+name); err != nil {
				t.Fatal(err)
			}
			if tt.grant != "" {
				if _, err := p.pool.Exec(t.Context(), "GRANT SELECT ("+tt.grant+") ON "+testTable+" TO "+name); err != nil {
					t.Fatal(err)
				}
			}
			err := reader.ValidateModel(t.Context(), testModel, testDims, testSignature)
			if err == nil || !strings.Contains(err.Error(), "42501") {
				t.Fatalf("ValidateModel with denied %s read = %v, want insufficient_privilege", tt.name, err)
			}
		})
	}
}
