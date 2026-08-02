// Content: the repository tarball, streamed once and extracted in memory.
//
// One request transfers every file, where fetching blobs individually would be one request per
// file against the same hourly quota. Nothing touches the filesystem, so there is no temporary
// directory to clean up and no path traversal to defend against — an archive entry naming
// ../../etc/passwd simply fails the want predicate and is discarded.
//
// Three bounds keep a hostile or merely enormous archive from exhausting memory: the compressed
// transfer is capped by limits.tarball_max_bytes, the total decompressed size of retained files
// is capped by the same number, and each file is capped by the caller's per-file limit. Hitting
// any of them stops extraction and reports truncation rather than failing, so a run indexes what
// it could read.
package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// archive is the extracted content of one repository, keyed by repository-relative path.
type archive struct {
	files     map[string][]byte
	truncated bool // a bound stopped extraction before the archive ended
}

// tarball downloads and extracts the archive of one ref, retaining only the paths want accepts.
func (c *Connector) tarball(ctx context.Context, fullName, ref string, want func(string) bool, maxFile int64) (archive, error) {
	target := fmt.Sprintf("repos/%s/tarball/%s", fullName, ref)
	body, err := c.client.stream(ctx, target)
	if err != nil {
		return archive{}, fmt.Errorf("downloading tarball of %s@%s: %w", fullName, ref, err)
	}
	defer func() { _ = body.Close() }()

	// The compressed bound is applied to the transfer itself, so a runaway archive is cut off
	// at the socket rather than after it has been decompressed.
	limited := io.LimitReader(body, c.limits.TarballMaxBytes)
	out, err := extract(limited, c.limits.TarballMaxBytes, maxFile, want)
	if err != nil {
		return archive{}, fmt.Errorf("extracting tarball of %s@%s: %w", fullName, ref, err)
	}
	return out, nil
}

// extract reads a gzipped tar stream and returns the retained files.
//
// maxTotal bounds the sum of retained content; maxFile bounds any single file. A file over
// maxFile is skipped here and recorded as truncated by the caller, which knows the size the tree
// reported and can say so without having read the content.
func extract(r io.Reader, maxTotal, maxFile int64, want func(string) bool) (archive, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return archive{}, fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()

	out := archive{files: map[string][]byte{}}
	var total int64
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			// An archive cut off by the compressed bound reports an unexpected EOF. That
			// is truncation, which is a warning, not a failed run.
			if errors.Is(err, io.ErrUnexpectedEOF) {
				out.truncated = true
				return out, nil
			}
			return archive{}, fmt.Errorf("reading archive entry: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		path := stripRoot(header.Name)
		if path == "" || !want(path) {
			continue
		}
		if header.Size > maxFile {
			continue
		}
		if total+header.Size > maxTotal {
			out.truncated = true
			return out, nil
		}
		content := make([]byte, header.Size)
		if _, err := io.ReadFull(reader, content); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				out.truncated = true
				return out, nil
			}
			return archive{}, fmt.Errorf("reading %s from archive: %w", path, err)
		}
		out.files[path] = content
		total += header.Size
	}
}

// stripRoot removes the single directory component GitHub wraps a tarball in — owner-repo-sha —
// leaving the repository-relative path the tree endpoint uses. An entry with no separator is the
// wrapper directory itself and yields "".
func stripRoot(name string) string {
	name = strings.TrimPrefix(name, "./")
	if index := strings.Index(name, "/"); index >= 0 {
		return name[index+1:]
	}
	return ""
}
