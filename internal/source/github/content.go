// Content-shaped noise filtering: what a path cannot tell you.
//
// keepPath (filter.go) decides from the path alone, which is what makes it cheap enough to run
// against a whole tree before anything is transferred. It cannot catch a heap dump named
// java_pid26365.hprof, a webpack bundle named a-bundle.js, or a wordlist named words.txt: those
// are ordinary-looking names holding content no reader would read. Deriving one costs the same
// generator call as a source file and produces a summary that says "this is a list of words".
//
// So a second filter runs once the bytes are in hand. It judges shape, not extension, which is
// the generic form of the question — a new dump format needs no new pattern — and it judges the
// whole file before splitting, so one dump is one decision rather than sixteen.
//
// Every threshold here is deliberately loose. A false negative costs one summary; a false
// positive silently drops something a person wrote, so the checks only fire on content that is
// unambiguously machine-shaped, and all but the binary check require the file to be large.
package github

import (
	"bytes"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	// sampleBytes is how much of a file the binary check inspects. Non-text content declares
	// itself in its header; reading further would not change the answer.
	sampleBytes = 8192

	// binaryRatio is the share of the sample that may be non-printable before the file is
	// treated as binary. It is not zero because legitimately textual files carry stray
	// control characters, and it is low because prose and code do not carry many.
	binaryRatio = 0.10

	// largeFileBytes is the floor for the shape checks. Below it a file is cheap enough that a
	// wrong verdict costs more in lost signal than the derivation costs in tokens.
	largeFileBytes = 262144

	// minifiedLineBytes is the line length that marks generated output. Hand-written lines are
	// bounded by what a person can read; bundlers and minifiers emit whole modules per line.
	minifiedLineBytes = 5000

	// listMeanLineBytes is the mean line length below which a large file is a list of records
	// rather than a document: a wordlist, an export, a table. It is low because real code
	// averages lower than it looks — a 275 KB hand-written Rust file in the sample corpus
	// averages 34 bytes a line, so a bound near 40 drops source people wrote.
	listMeanLineBytes = 16
)

// bulkExtensions are the extensions where large size means bulk records rather than a large
// document. Nobody hand-writes 256 KB of JSON or a quarter-megabyte text file; a source file of
// that size is a big module, which is why code extensions are absent from this list.
var bulkExtensions = map[string]bool{
	".json": true, ".geojson": true, ".txt": true, ".xml": true, ".xsd": true,
	".yaml": true, ".yml": true, ".sql": true,
}

// exclusionReason returns why content should not become a fragment, or "" to keep it.
//
// The reason is recorded on the fragment and reported in the manifest, so a repository that
// loses a file to this filter says which file and why rather than quietly indexing less.
func exclusionReason(key string, content []byte) string {
	if len(content) == 0 {
		return ""
	}
	if looksBinary(content) {
		return "binary"
	}
	if len(content) < largeFileBytes {
		return ""
	}
	if longestLine(content) > minifiedLineBytes {
		return "generated"
	}
	if meanLineLen(content) < listMeanLineBytes || bulkExtensions[strings.ToLower(path.Ext(splitKey(key)))] {
		return "data"
	}
	return ""
}

// splitKey strips the "#N" suffix fileFragments adds to a piece of a split file, so that the
// extension of a piece is the extension of the file it came from.
func splitKey(key string) string {
	if hash := strings.LastIndexByte(key, '#'); hash > 0 {
		return key[:hash]
	}
	return key
}

// looksBinary reports whether a sample of content is not text: a NUL byte, invalid UTF-8, or
// too many control characters.
func looksBinary(content []byte) bool {
	sample := content
	if len(sample) > sampleBytes {
		sample = sample[:sampleBytes]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	nonPrintable := 0
	for i, count := 0, 0; i < len(sample); count++ {
		r, size := utf8.DecodeRune(sample[i:])
		// A truncated rune at the end of the sample is the cut, not an encoding error.
		if r == utf8.RuneError && size == 1 && len(sample)-i >= utf8.UTFMax {
			return true
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			nonPrintable++
		}
		i += size
	}
	return float64(nonPrintable) > float64(len(sample))*binaryRatio
}

// longestLine returns the length in bytes of the longest line in content.
func longestLine(content []byte) int {
	longest, start := 0, 0
	for {
		next := bytes.IndexByte(content[start:], '\n')
		if next < 0 {
			return max(longest, len(content)-start)
		}
		longest = max(longest, next)
		start += next + 1
	}
}

// meanLineLen returns the mean line length in bytes, counting a file with no newline as one line.
func meanLineLen(content []byte) int {
	lines := bytes.Count(content, []byte{'\n'}) + 1
	return len(content) / lines
}
