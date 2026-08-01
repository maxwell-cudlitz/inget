// Key layout tests. These pin the on-disk contract: a change that breaks one of these
// breaks every store already written.
package artifact

import (
	"strings"
	"testing"
)

func TestKeyLayout(t *testing.T) {
	digest := "3f8a91c2" + strings.Repeat("0", digestHexLen-8)

	if got, want := blobKey(digest), "blobs/3f/8a/"+digest; got != want {
		t.Errorf("blobKey = %q, want %q", got, want)
	}
	// The datatype's slash is flattened, so a run directory is one level deep per
	// datatype and listing runs stays unambiguous.
	if got, want := runPrefix("github", "github/repo", validRunID), "runs/github/github_repo/"+validRunID+"/"; got != want {
		t.Errorf("runPrefix = %q, want %q", got, want)
	}
	if got, want := datatypePrefix("monday", "monday/item"), "runs/monday/monday_item/"; got != want {
		t.Errorf("datatypePrefix = %q, want %q", got, want)
	}
}

func TestShardName(t *testing.T) {
	tests := []struct {
		index       int
		compression Compression
		want        string
	}{
		{0, CompressionZstd, "records-00000.jsonl.zst"},
		{7, CompressionZstd, "records-00007.jsonl.zst"},
		{1234, CompressionNone, "records-01234.jsonl"},
		// Five digits keep lexical order equal to numeric order well past any real run.
		{99999, CompressionZstd, "records-99999.jsonl.zst"},
	}
	for _, tt := range tests {
		if got := shardName(tt.index, tt.compression); got != tt.want {
			t.Errorf("shardName(%d, %s) = %q, want %q", tt.index, tt.compression, got, tt.want)
		}
	}
}

func TestCheckDigest(t *testing.T) {
	tests := []struct {
		name   string
		digest string
		ok     bool
	}{
		{"valid", strings.Repeat("a1", digestHexLen/2), true},
		{"empty", "", false},
		{"too short", "abc123", false},
		{"too long", strings.Repeat("a", digestHexLen+1), false},
		// Uppercase would address a different key on a case-sensitive store, so the
		// encoding is pinned rather than normalized.
		{"uppercase", strings.Repeat("A1", digestHexLen/2), false},
		{"non hex", strings.Repeat("z", digestHexLen), false},
		{"path traversal", "../" + strings.Repeat("a", digestHexLen-3), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkDigest(tt.digest)
			if tt.ok && err != nil {
				t.Errorf("checkDigest(%q) = %v, want nil", tt.digest, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("checkDigest(%q) = nil, want an error", tt.digest)
			}
		})
	}
}

// TestCheckShardPath proves a manifest cannot direct a read outside its run directory. A
// manifest is data from a store that may be shared, so its paths are untrusted.
func TestCheckShardPath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"records-00000.jsonl.zst", true},
		{"records-00000.jsonl", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../../etc/passwd", false},
		{"nested/records-00000.jsonl.zst", false},
		{`windows\path`, false},
	}
	for _, tt := range tests {
		err := checkShardPath(tt.path)
		if tt.ok && err != nil {
			t.Errorf("checkShardPath(%q) = %v, want nil", tt.path, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("checkShardPath(%q) = nil, want an error", tt.path)
		}
	}
}

func TestCompressionValidate(t *testing.T) {
	tests := []struct {
		in   Compression
		want Compression
		ok   bool
	}{
		{"", CompressionZstd, true}, // absent means the default
		{CompressionZstd, CompressionZstd, true},
		{CompressionNone, CompressionNone, true},
		{"gzip", "", false},
	}
	for _, tt := range tests {
		got, err := tt.in.validate()
		if tt.ok && (err != nil || got != tt.want) {
			t.Errorf("Compression(%q).validate() = %q, %v; want %q, nil", tt.in, got, err, tt.want)
		}
		if !tt.ok && err == nil {
			t.Errorf("Compression(%q).validate() = nil error, want a rejection", tt.in)
		}
	}
}
