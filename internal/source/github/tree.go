// The recursive tree endpoint, which is where this connector's incrementality comes from.
//
// One request per repository returns every path with its git blob SHA — an exact content hash,
// obtained without transferring any content. That is the level-1 fingerprint, so a file whose
// SHA is unchanged needs neither an upload nor a derivation, and the consumer can tell which
// files changed before anything is downloaded.
//
// The endpoint has a hard limit: a tree over roughly 100,000 entries or 7 MB of response comes
// back with truncated: true and a partial list. That is reported as a warning rather than an
// error, because a partial view of a very large monorepo is still worth indexing and failing
// the run would mean indexing none of it.
//
// Reference: https://docs.github.com/en/rest/git/trees
package github

import (
	"context"
	"fmt"
	"net/url"
)

// Git object types the tree endpoint reports. Only blobs are files; trees are directories and
// commits are submodule pointers, neither of which has content here.
const objectBlob = "blob"

// treeEntry is one path in a repository tree.
type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// treeResponse is the recursive tree of one ref.
type treeResponse struct {
	SHA       string      `json:"sha"`
	Tree      []treeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

// tree fetches the recursive tree of one ref.
func (c *Connector) tree(ctx context.Context, fullName, ref string) (treeResponse, error) {
	target := fmt.Sprintf("repos/%s/git/trees/%s?%s",
		fullName, url.PathEscape(ref), url.Values{"recursive": {"1"}}.Encode())
	var out treeResponse
	if err := c.client.getJSON(ctx, target, &out); err != nil {
		return treeResponse{}, fmt.Errorf("reading tree of %s@%s: %w", fullName, ref, err)
	}
	return out, nil
}

// blobs returns the file entries of a tree, dropping directories and submodule pointers.
func (t treeResponse) blobs() []treeEntry {
	files := make([]treeEntry, 0, len(t.Tree))
	for _, entry := range t.Tree {
		if entry.Type == objectBlob && entry.Path != "" {
			files = append(files, entry)
		}
	}
	return files
}
