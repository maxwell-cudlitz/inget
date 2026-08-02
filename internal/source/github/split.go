// Sub-file fragments, so that incrementality survives below file granularity.
//
// A file over the blob size cap could be dropped, but a 3 MB generated schema or a long
// changelog is often the most informative thing in a repository. Splitting it into
// independently fingerprinted pieces means a change to one section re-derives one piece
// instead of the whole file, and means the file is indexed at all.
//
// Splits are deterministic and content-derived: the same bytes always produce the same pieces
// with the same keys, and each piece is fingerprinted by the SHA-256 of its own content. A
// whole-file git blob SHA cannot serve here, because every piece of a file would share it and
// the cascade could not tell which piece changed.
//
// Cuts prefer the last newline inside the window, so a piece ends at a line boundary wherever
// the content has lines at all. That keeps a piece readable to a model and keeps the cut point
// stable under edits elsewhere in the file.
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/maxwell-cudlitz/inget/internal/source"
)

// maxParts bounds how many pieces one file may become. A file large enough to exceed it is
// recorded as truncated with no content: past this size the content is a data file rather than
// something a reader would read, and the pieces would crowd out the rest of the repository
// under max_fragments_per_item.
const maxParts = 16

// fileFragments turns one file into one fragment, or into numbered pieces when it exceeds max.
//
// The single-fragment case keeps the git blob SHA as its fingerprint: it is an exact content
// hash the tree endpoint already supplied, so there is nothing to gain by recomputing one.
func fileFragments(key, blobSHA string, content []byte, tier int, max int64) []source.Fragment {
	mime := mimeType(key)
	if max <= 0 || int64(len(content)) <= max {
		return []source.Fragment{{
			Key:         key,
			Fingerprint: blobSHA,
			Content:     content,
			Bytes:       int64(len(content)),
			Tier:        tier,
			MIME:        mime,
		}}
	}

	pieces := splitContent(content, int(max))
	out := make([]source.Fragment, 0, len(pieces))
	for i, piece := range pieces {
		out = append(out, source.Fragment{
			Key:         fmt.Sprintf("%s#%d", key, i),
			Fingerprint: contentHash(piece),
			Content:     piece,
			Bytes:       int64(len(piece)),
			Tier:        tier,
			MIME:        mime,
			Meta: map[string]string{
				"part":       fmt.Sprint(i),
				"parts":      fmt.Sprint(len(pieces)),
				"split_of":   key,
				"file_bytes": fmt.Sprint(len(content)),
			},
		})
	}
	return out
}

// splitContent cuts content into pieces of at most max bytes, preferring the last newline in
// each window. A window with no newline is cut at max, which is the honest answer for a single
// enormous line: any other cut point would be equally arbitrary and less predictable.
func splitContent(content []byte, max int) [][]byte {
	if max <= 0 {
		return [][]byte{content}
	}
	var pieces [][]byte
	for len(content) > max {
		cut := max
		if newline := lastNewline(content[:max]); newline > 0 {
			cut = newline
		}
		pieces = append(pieces, content[:cut])
		content = content[cut:]
	}
	if len(content) > 0 {
		pieces = append(pieces, content)
	}
	return pieces
}

// lastNewline returns the index just past the last newline in window, or 0 when there is none.
func lastNewline(window []byte) int {
	for i := len(window) - 1; i >= 0; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// contentHash is the SHA-256 of content, hex encoded. It is a change token, not a blob key: the
// blob store computes its own digest over the same bytes and the two agreeing is incidental.
func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// splitBudget is the largest file worth reading in order to split it. Beyond this the file is
// recorded as truncated without being transferred.
func splitBudget(max int64) int64 {
	if max <= 0 {
		return 0
	}
	return max * maxParts
}
