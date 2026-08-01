// The store: one gocloud.dev bucket holding content-addressed blobs and run directories.
//
// Content addressing is what makes an incremental fetch cheap. Identical fragment content
// is stored once no matter how many items or runs reference it, and the producer checks
// existence before writing, so a repository where one file changed uploads exactly one
// blob (D5).
//
// One bucket abstraction serves file://, s3:// and gs:// (D15), which keeps the backend a
// configuration decision rather than a code path. The cost is dependency weight; see
// drivers.go.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

// Documented defaults for the sizing limits, matching the shipped config.yaml. They are
// repeated here so a caller constructing Options directly — a test, or a future tool —
// gets the same behavior as a configured run.
const (
	DefaultShardTargetBytes int64 = 128 << 20 // 128 MiB uncompressed per shard
	DefaultBlobMaxBytes     int64 = 1 << 20   // per-fragment content cap
)

// Sentinel errors callers are expected to branch on.
var (
	// ErrBlobTooLarge reports content above BlobMaxBytes. The producer responds by
	// recording the fragment as truncated with no blob, not by failing the run.
	ErrBlobTooLarge = errors.New("content exceeds blob_max_bytes")

	// ErrNotFound reports a missing key: a blob a record references, or a run that was
	// garbage collected.
	ErrNotFound = errors.New("not found")
)

// Options configures a Store. It mirrors the artifacts block of the configuration; see
// FromConfig for the bridge.
type Options struct {
	URL              string      // file://, s3:// or gs://
	ShardTargetBytes int64       // uncompressed target before rolling to a new shard
	BlobMaxBytes     int64       // per-blob content cap
	Compression      Compression // zstd (default) or none
}

// normalize fills in defaults and rejects values that cannot work.
func (o Options) normalize() (Options, error) {
	if o.URL == "" {
		return o, errors.New("artifact store URL is required")
	}
	if o.ShardTargetBytes <= 0 {
		o.ShardTargetBytes = DefaultShardTargetBytes
	}
	if o.BlobMaxBytes <= 0 {
		o.BlobMaxBytes = DefaultBlobMaxBytes
	}
	compression, err := o.Compression.validate()
	if err != nil {
		return o, err
	}
	o.Compression = compression
	return o, nil
}

// Store reads and writes one artifact store. It is safe for concurrent use.
type Store struct {
	bucket *blob.Bucket
	codec  *codec
	opts   Options
}

// Open connects to the store described by opts. The caller owns the result and must Close
// it; for file:// URLs the target directory is created if it does not exist.
func Open(ctx context.Context, opts Options) (*Store, error) {
	opts, err := opts.normalize()
	if err != nil {
		return nil, err
	}
	bucketURL, err := bucketURL(opts.URL)
	if err != nil {
		return nil, err
	}
	bucket, err := blob.OpenBucket(ctx, bucketURL)
	if err != nil {
		return nil, fmt.Errorf("opening artifact store %s: %w", opts.URL, err)
	}
	codec, err := newCodec(opts.Compression, opts.BlobMaxBytes)
	if err != nil {
		_ = bucket.Close() // the codec failure is the one worth reporting
		return nil, err
	}
	return &Store{bucket: bucket, codec: codec, opts: opts}, nil
}

// Close releases the bucket and the codec.
func (s *Store) Close() error {
	s.codec.close()
	if err := s.bucket.Close(); err != nil {
		return fmt.Errorf("closing artifact store: %w", err)
	}
	return nil
}

// bucketURL adapts the configured URL to the one gocloud.dev needs.
//
// Filesystem stores get three defaults, each overridable by naming the parameter in the
// configured URL: create_dir, because a fresh checkout has no .inget directory;
// no_tmp_dir, because the default temporary directory may be on another device and the
// rename that publishes a write would then fail with a cross-device link error; and
// metadata=skip, because the sidecar .attrs file the driver writes otherwise would double
// the object count and appear in listings that this package treats as run directories.
func bucketURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("artifact store URL %q: %w", raw, err)
	}
	if u.Scheme != "file" {
		return raw, nil
	}
	q := u.Query()
	for key, value := range map[string]string{
		"create_dir": "true",
		"no_tmp_dir": "true",
		"metadata":   "skip",
	} {
		if !q.Has(key) {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// HasBlob reports whether content with the given digest is already stored. The producer
// calls this before spending bandwidth on an upload.
func (s *Store) HasBlob(ctx context.Context, digest string) (bool, error) {
	if err := checkDigest(digest); err != nil {
		return false, err
	}
	exists, err := s.bucket.Exists(ctx, blobKey(digest))
	if err != nil {
		return false, fmt.Errorf("checking blob %s: %w", digest, err)
	}
	return exists, nil
}

// PutBlob stores content and returns its digest, reporting whether a write happened. A
// false written value means the digest was already present, which is the counter behind
// blobs_reused in the manifest.
//
// Content above BlobMaxBytes returns ErrBlobTooLarge and writes nothing; the caller
// records the fragment as truncated with no blob.
func (s *Store) PutBlob(ctx context.Context, content []byte) (digest string, written bool, err error) {
	if int64(len(content)) > s.opts.BlobMaxBytes {
		return "", false, fmt.Errorf("%w (%d bytes)", ErrBlobTooLarge, len(content))
	}
	sum := sha256.Sum256(content)
	digest = hex.EncodeToString(sum[:])

	exists, err := s.HasBlob(ctx, digest)
	if err != nil {
		return "", false, err
	}
	if exists {
		return digest, false, nil
	}
	if err := s.bucket.WriteAll(ctx, blobKey(digest), s.codec.encode(content), nil); err != nil {
		return "", false, fmt.Errorf("writing blob %s: %w", digest, err)
	}
	return digest, true, nil
}

// GetBlob returns the uncompressed content for a digest, verifying that what came back
// hashes to what was asked for. The check is cheap at these sizes and turns silent
// corruption into a failed run.
func (s *Store) GetBlob(ctx context.Context, digest string) ([]byte, error) {
	if err := checkDigest(digest); err != nil {
		return nil, err
	}
	stored, err := s.read(ctx, blobKey(digest))
	if err != nil {
		return nil, err
	}
	content, err := s.codec.decode(stored)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", digest, err)
	}
	if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("blob %s: content does not match its digest", digest)
	}
	return content, nil
}

// read fetches a key, translating a backend "missing" into ErrNotFound.
func (s *Store) read(ctx context.Context, key string) ([]byte, error) {
	data, err := s.bucket.ReadAll(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	return data, nil
}
