// Unit tests for the parts that need no database: placeholder rewriting, option
// defaulting, and the configuration bridge.
package state

import (
	"strings"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/config"
)

func TestRebind(t *testing.T) {
	tests := []struct {
		name    string
		dialect string
		query   string
		want    string
	}{
		{
			name:    "sqlite leaves question marks alone",
			dialect: DriverSQLite,
			query:   "SELECT a FROM t WHERE b = ? AND c = ?",
			want:    "SELECT a FROM t WHERE b = ? AND c = ?",
		},
		{
			name:    "postgres numbers placeholders in order",
			dialect: DriverPostgres,
			query:   "SELECT a FROM t WHERE b = ? AND c = ?",
			want:    "SELECT a FROM t WHERE b = $1 AND c = $2",
		},
		{
			name:    "a statement without placeholders is untouched",
			dialect: DriverPostgres,
			query:   "SELECT count(*) FROM t",
			want:    "SELECT count(*) FROM t",
		},
		{
			name:    "a question mark inside a literal is not a placeholder",
			dialect: DriverPostgres,
			query:   "SELECT a FROM t WHERE b = ? AND c = 'why?' AND d = ?",
			want:    "SELECT a FROM t WHERE b = $1 AND c = 'why?' AND d = $2",
		},
		{
			name:    "an escaped quote inside a literal does not unbalance the scan",
			dialect: DriverPostgres,
			query:   "SELECT a FROM t WHERE b = 'it''s' AND c = ?",
			want:    "SELECT a FROM t WHERE b = 'it''s' AND c = $1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := lookupDialect(tt.dialect)
			if err != nil {
				t.Fatalf("lookupDialect: %v", err)
			}
			if got := d.rebind(tt.query); got != tt.want {
				t.Errorf("rebind = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every statement in the package is written with ? placeholders, so a stray $1 would work
// on postgres and silently fail on sqlite. This asserts the convention rather than trusting
// it, because the failure would only show up on the non-default driver.
func TestStatementsUseQuestionMarkPlaceholders(t *testing.T) {
	statements := map[string]string{
		"itemUpsert":     itemUpsertSQL,
		"itemSelect":     itemSelectSQL,
		"fragmentUpsert": fragmentUpsertSQL,
		"derivationHit":  derivationHitSQL,
		"viewUpsert":     viewUpsertSQL,
		"refInsert":      refInsertSQL,
		"runStart":       runStartSQL,
		"runResumable":   runResumableSQL,
		"workClaim":      workClaimSQL,
		"workComplete":   workCompleteSQL,
		"advisoryLock":   advisoryLockSQL,
		"mutexLock":      mutexLockSQL,
	}
	for name, query := range statements {
		if strings.Contains(query, "$1") {
			t.Errorf("%s uses an ordinal placeholder; write ? and let the dialect rebind", name)
		}
	}
}

func TestLookupDialectRejectsUnknownDriver(t *testing.T) {
	if _, err := lookupDialect("mysql"); err == nil {
		t.Error("lookupDialect(mysql): want an error")
	}
}

func TestOptionsNormalize(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		want    Options
		wantErr bool
	}{
		{
			name:    "a driver is required",
			opts:    Options{},
			wantErr: true,
		},
		{
			name:    "postgres without a DSN is rejected",
			opts:    Options{Driver: DriverPostgres},
			wantErr: true,
		},
		{
			name: "postgres keeps its DSN",
			opts: Options{Driver: DriverPostgres, DSN: "postgres://localhost/inget"},
			want: Options{Driver: DriverPostgres, DSN: "postgres://localhost/inget"},
		},
		{
			name: "sqlite defaults its path",
			opts: Options{Driver: DriverSQLite},
			want: Options{Driver: DriverSQLite, Path: DefaultSQLitePath},
		},
		{
			name: "an explicit sqlite path is kept",
			opts: Options{Driver: DriverSQLite, Path: "/tmp/state.db"},
			want: Options{Driver: DriverSQLite, Path: "/tmp/state.db"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.opts.normalize()
			if tt.wantErr {
				if err == nil {
					t.Fatal("normalize: want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalize = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFromConfig(t *testing.T) {
	t.Run("sqlite needs no secret", func(t *testing.T) {
		cfg := &config.Config{State: config.State{Driver: DriverSQLite, Path: "./state.db"}}
		got, err := FromConfig(cfg)
		if err != nil {
			t.Fatalf("FromConfig: %v", err)
		}
		if got.Driver != DriverSQLite || got.Path != "./state.db" || got.DSN != "" {
			t.Errorf("FromConfig = %+v, want the sqlite path and no DSN", got)
		}
	})

	t.Run("postgres reports a missing DSN variable", func(t *testing.T) {
		cfg := &config.Config{State: config.State{Driver: DriverPostgres, DSNEnv: "INGET_STATE_DSN"}}
		if _, err := FromConfig(cfg); err == nil {
			t.Error("FromConfig without the DSN variable set: want an error")
		}
	})
}
