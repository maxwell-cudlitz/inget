// Tests for the content-shape filter.
//
// The cases pin the two directions that matter and are easy to break by moving a threshold: the
// noise this filter exists for is dropped whatever it is named, and hand-written content is kept
// even when it is large. Sizes are built rather than fixtured so the thresholds under test are
// visible in the test.
package github

import (
	"strings"
	"testing"
)

// repeatLines builds content of at least minBytes made of lines of the given length.
func repeatLines(lineLen, minBytes int) []byte {
	line := strings.Repeat("a", lineLen) + "\n"
	return []byte(strings.Repeat(line, minBytes/len(line)+1))
}

func TestExclusionReason(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		content []byte
		want    string
	}{
		{"empty", "empty.go", nil, ""},
		{"source file", "main.go", []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), ""},
		// A heap dump: an ordinary name, and NUL bytes in the first block.
		{"binary", "dump.hprof", append([]byte("JAVA PROFILE 1.0.2\x00\x00\x00"), make([]byte, 64)...), "binary"},
		{"binary beats size", "small.go", []byte("\x00short"), "binary"},
		// A bundle: one enormous line, over the size floor.
		{"minified", "app.js", repeatLines(minifiedLineBytes+1, largeFileBytes), "generated"},
		// A wordlist: short lines, over the size floor.
		{"wordlist", "words.dic", repeatLines(6, largeFileBytes), "data"},
		// The same shapes below the floor are kept: a small file is not worth a wrong verdict.
		{"small wordlist", "words.dic", repeatLines(6, 1024), ""},
		// Prose and code stay well above the mean-line floor at any size.
		{"large document", "guide.md", repeatLines(90, largeFileBytes), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exclusionReason(tt.key, tt.content); got != tt.want {
				t.Errorf("exclusionReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// UTF-8 text is not binary because its bytes are not ASCII. The check reads runes for exactly
// this case.
func TestExclusionReasonKeepsMultiByteText(t *testing.T) {
	content := []byte(strings.Repeat("日本語のドキュメントです。\n", 40))
	if got := exclusionReason("docs/guide.md", content); got != "" {
		t.Errorf("exclusionReason() = %q, want %q", got, "")
	}
}

func TestLongestLineAndMean(t *testing.T) {
	content := []byte("ab\ncdef\ng\n")
	if got := longestLine(content); got != 4 {
		t.Errorf("longestLine() = %d, want 4", got)
	}
	// Ten bytes over four lines, the last being the empty remainder after the final newline.
	if got := meanLineLen(content); got != 2 {
		t.Errorf("meanLineLen() = %d, want 2", got)
	}
}
