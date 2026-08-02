// Coverage for options normalization, the driver registry, the configuration bridge and
// extension version comparison. None of this opens a connection.
package destination

import (
	"strings"
	"testing"
)

// fullOptions returns options that normalize without changing anything.
func fullOptions() Options {
	return Options{
		Driver:         DriverPGVector,
		DSN:            "postgres://localhost/inget",
		Table:          "inget_vectors",
		Storage:        StorageHalfvec,
		Dims:           1024,
		M:              24,
		EFConstruction: 200,
		EFSearch:       100,
		Granularity:    GranularityItem,
		BatchSize:      500,
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	in := fullOptions()
	in.Storage, in.Granularity, in.BatchSize = "", "", 0

	got, err := in.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got.Storage != StorageHalfvec {
		t.Errorf("Storage = %q, want %q", got.Storage, StorageHalfvec)
	}
	if got.Granularity != GranularityItem {
		t.Errorf("Granularity = %q, want %q", got.Granularity, GranularityItem)
	}
	if got.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", got.BatchSize, DefaultBatchSize)
	}
}

func TestNormalizeRejectsBadOptions(t *testing.T) {
	cases := []struct {
		name   string
		spoil  func(*Options)
		expect string
	}{
		{"no driver", func(o *Options) { o.Driver = "" }, "driver"},
		{"no DSN", func(o *Options) { o.DSN = "" }, "DSN"},
		{"no table", func(o *Options) { o.Table = "" }, "table"},
		{"quoted table", func(o *Options) { o.Table = `vectors"; DROP TABLE x --` }, "identifier"},
		{"uppercase table", func(o *Options) { o.Table = "IngetVectors" }, "identifier"},
		{"table starting with a digit", func(o *Options) { o.Table = "1vectors" }, "identifier"},
		{"overlong table", func(o *Options) { o.Table = strings.Repeat("v", 64) }, "63-byte"},
		{"unknown storage", func(o *Options) { o.Storage = "float8" }, "storage"},
		{"zero dims", func(o *Options) { o.Dims = 0 }, "dims"},
		{"unknown granularity", func(o *Options) { o.Granularity = "chunk" }, "granularity"},
		{"zero hnsw.m", func(o *Options) { o.M = 0 }, "hnsw.m"},
		{"zero ef_construction", func(o *Options) { o.EFConstruction = 0 }, "ef_construction"},
		{"zero ef_search", func(o *Options) { o.EFSearch = 0 }, "ef_search"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			o := fullOptions()
			tt.spoil(&o)
			_, err := o.normalize()
			if err == nil {
				t.Fatalf("normalize(%s): want an error", tt.name)
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Errorf("error = %q, want it to mention %q", err, tt.expect)
			}
		})
	}
}

func TestOpenRejectsAnUnknownDriver(t *testing.T) {
	o := fullOptions()
	o.Driver = "qdrant"
	_, err := Open(t.Context(), o)
	if err == nil {
		t.Fatal("Open with an unknown driver: want an error")
	}
	// The message lists what is available, which is the only actionable part of it.
	if !strings.Contains(err.Error(), DriverPGVector) {
		t.Errorf("error = %q, want it to list %q", err, DriverPGVector)
	}
}

func TestPGVectorIsRegistered(t *testing.T) {
	names := registered()
	for _, name := range names {
		if name == DriverPGVector {
			return
		}
	}
	t.Errorf("registered() = %v, want it to include %q", names, DriverPGVector)
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.8.0", "0.8.0", 0},
		{"0.8", "0.8.0", 0}, // absent parts count as zero
		{"0.10", "0.8", 1},  // the reason this is not a string comparison
		{"0.7.4", "0.8.0", -1},
		{"1.0", "0.8.0", 1},
		{"0.8.0-dev", "0.8.0", 0}, // a suffix is not a version part
		{"", "0.8.0", -1},
	}
	for _, tt := range cases {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			if got := compareVersions(tt.a, tt.b); got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
