// One record shard, on its way out.
//
// A shard is written once, streaming, and its manifest entry has to describe the object
// exactly: record count, uncompressed and compressed sizes, and the SHA-256 of the
// compressed bytes a reader will verify. Computing all of that as the bytes pass through
// means the manifest can be written without re-reading the object, which on an object
// store would be a second full transfer.
//
// The chain is: JSON line -> compression encoder -> counter -> {object, digest}.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"gocloud.dev/blob"
)

// shardWriter streams one record shard, hashing and measuring the compressed bytes on
// their way to the bucket.
type shardWriter struct {
	name         string
	object       *blob.Writer
	encoder      io.WriteCloser
	digest       hash.Hash
	compressed   *countingWriter
	records      int
	uncompressed int64
}

// newShard opens the index-th shard of a run.
func (s *Store) newShard(ctx context.Context, prefix string, index int) (*shardWriter, error) {
	name := shardName(index, s.opts.Compression)
	object, err := s.bucket.NewWriter(ctx, prefix+name, nil)
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", prefix+name, err)
	}
	digest := sha256.New()
	compressed := &countingWriter{w: io.MultiWriter(object, digest)}
	encoder, err := s.codec.newShardWriter(compressed)
	if err != nil {
		_ = object.Close()
		return nil, err
	}
	return &shardWriter{
		name:       name,
		object:     object,
		encoder:    encoder,
		digest:     digest,
		compressed: compressed,
	}, nil
}

// write appends one already-encoded JSONL line.
func (w *shardWriter) write(line []byte) error {
	if _, err := w.encoder.Write(line); err != nil {
		return fmt.Errorf("writing to %s: %w", w.name, err)
	}
	w.records++
	w.uncompressed += int64(len(line))
	return nil
}

// finish flushes the compression frame, commits the object, and returns its manifest
// entry. The object is closed even when flushing failed, so a failure cannot leak the
// underlying writer.
func (w *shardWriter) finish() (Shard, error) {
	encErr := w.encoder.Close()
	objErr := w.object.Close()
	if err := errors.Join(encErr, objErr); err != nil {
		return Shard{}, fmt.Errorf("closing %s: %w", w.name, err)
	}
	return Shard{
		Path:              w.name,
		Records:           w.records,
		BytesCompressed:   w.compressed.n,
		BytesUncompressed: w.uncompressed,
		SHA256:            hex.EncodeToString(w.digest.Sum(nil)),
	}, nil
}

// countingWriter tallies bytes on their way through.
type countingWriter struct {
	w io.Writer
	n int64
}

// Write forwards p and adds what was accepted to the tally.
func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // transparent pass-through of the underlying writer
}
