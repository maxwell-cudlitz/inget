// Tests for sub-file fragments.
//
// Three properties matter and each has a case here: pieces reassemble to the original bytes, cuts
// land on line boundaries where the content has lines, and the same input always produces the same
// keys and fingerprints. The third is what the cascade depends on — a split that shifted on every
// fetch would invalidate every piece of every large file forever.
package github

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSplitContentReassembles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		max     int
		pieces  int
	}{
		{"under the limit", "one line\n", 100, 1},
		{"exactly the limit", strings.Repeat("a", 10), 10, 1},
		{"two line-aligned pieces", "aaaa\nbbbb\ncccc\n", 10, 2},
		{"no newline forces a hard cut", strings.Repeat("x", 25), 10, 3},
		{"empty content", "", 10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pieces := splitContent([]byte(tt.content), tt.max)
			if len(pieces) != tt.pieces {
				t.Fatalf("splitContent() produced %d pieces, want %d", len(pieces), tt.pieces)
			}
			if got := string(bytes.Join(pieces, nil)); got != tt.content {
				t.Errorf("pieces reassemble to %q, want the original %q", got, tt.content)
			}
			for i, piece := range pieces {
				if len(piece) > tt.max {
					t.Errorf("piece %d is %d bytes, over the %d limit", i, len(piece), tt.max)
				}
			}
		})
	}
}

func TestSplitContentCutsOnLineBoundaries(t *testing.T) {
	content := []byte("alpha\nbeta\ngamma\ndelta\n")
	pieces := splitContent(content, 12)
	for i, piece := range pieces {
		if i == len(pieces)-1 {
			continue
		}
		if piece[len(piece)-1] != '\n' {
			t.Errorf("piece %d = %q, want it to end at a line boundary", i, piece)
		}
	}
}

// A file at or under the cap stays one fragment and keeps the git blob SHA the tree endpoint
// already supplied, because recomputing an exact content hash buys nothing.
func TestFileFragmentsKeepsBlobSHAWhenUnsplit(t *testing.T) {
	frags := fileFragments("README.md", "abc123", []byte("# Title\n"), 0, 1024)
	if len(frags) != 1 {
		t.Fatalf("fileFragments() produced %d fragments, want 1", len(frags))
	}
	if frags[0].Key != "README.md" {
		t.Errorf("key = %q, want the unsuffixed path", frags[0].Key)
	}
	if frags[0].Fingerprint != "abc123" {
		t.Errorf("fingerprint = %q, want the git blob SHA", frags[0].Fingerprint)
	}
	if frags[0].MIME != "text/markdown" {
		t.Errorf("mime = %q, want text/markdown", frags[0].MIME)
	}
}

func TestFileFragmentsSplitsOversizedFiles(t *testing.T) {
	var content strings.Builder
	for i := range 200 {
		fmt.Fprintf(&content, "line %03d padding padding padding\n", i)
	}
	raw := []byte(content.String())

	frags := fileFragments("docs/big.md", "blobsha", raw, 0, 1024)
	if len(frags) < 2 {
		t.Fatalf("fileFragments() produced %d fragments, want several", len(frags))
	}

	var rejoined []byte
	seen := map[string]bool{}
	for i, f := range frags {
		wantKey := fmt.Sprintf("docs/big.md#%d", i)
		if f.Key != wantKey {
			t.Errorf("fragment %d key = %q, want %q", i, f.Key, wantKey)
		}
		if f.Fingerprint == "blobsha" {
			t.Errorf("fragment %d reused the whole-file SHA, which cannot distinguish pieces", i)
		}
		if seen[f.Fingerprint] {
			t.Errorf("fragment %d repeats a fingerprint, so a change to one piece would be invisible", i)
		}
		seen[f.Fingerprint] = true
		if f.Meta["split_of"] != "docs/big.md" {
			t.Errorf("fragment %d meta = %v, want split_of naming the file", i, f.Meta)
		}
		if f.Meta["parts"] != fmt.Sprint(len(frags)) {
			t.Errorf("fragment %d parts = %q, want %d", i, f.Meta["parts"], len(frags))
		}
		if f.Bytes != int64(len(f.Content)) {
			t.Errorf("fragment %d bytes = %d, want %d", i, f.Bytes, len(f.Content))
		}
		rejoined = append(rejoined, f.Content...)
	}
	if !bytes.Equal(rejoined, raw) {
		t.Error("split fragments do not reassemble to the original file")
	}
}

// The same bytes must always produce the same pieces. Anything else re-derives every piece of
// every large file on every fetch.
func TestFileFragmentsAreDeterministic(t *testing.T) {
	raw := []byte(strings.Repeat("stable content line\n", 500))
	first := fileFragments("docs/big.md", "sha", raw, 0, 512)
	second := fileFragments("docs/big.md", "sha", raw, 0, 512)

	if len(first) != len(second) {
		t.Fatalf("piece counts differ: %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Key != second[i].Key || first[i].Fingerprint != second[i].Fingerprint {
			t.Errorf("piece %d differs between runs: %q/%q and %q/%q",
				i, first[i].Key, first[i].Fingerprint, second[i].Key, second[i].Fingerprint)
		}
	}
}

// A change to one piece must leave the other pieces' fingerprints alone, which is the whole
// reason splitting exists.
func TestSplitIsolatesChangeToOnePiece(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d padding padding\n", i)
	}
	before := fileFragments("docs/big.md", "sha", []byte(strings.Join(lines, "")), 0, 512)

	lines[len(lines)-1] = "line 199 CHANGED padding\n"
	after := fileFragments("docs/big.md", "sha", []byte(strings.Join(lines, "")), 0, 512)

	if len(before) != len(after) {
		t.Fatalf("piece counts differ after an edit: %d and %d", len(before), len(after))
	}
	changed := 0
	for i := range before {
		if before[i].Fingerprint != after[i].Fingerprint {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("%d pieces changed after editing one line, want exactly 1", changed)
	}
}

func TestSplitBudget(t *testing.T) {
	if got := splitBudget(1024); got != 1024*maxParts {
		t.Errorf("splitBudget(1024) = %d, want %d", got, 1024*maxParts)
	}
	if got := splitBudget(0); got != 0 {
		t.Errorf("splitBudget(0) = %d, want 0", got)
	}
}
