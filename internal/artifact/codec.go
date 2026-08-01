// Compression framing, shared by blobs and record shards.
//
// zstd is the default (D15): pure Go, and a better ratio and speed than gzip on JSONL.
// The artifacts.compression setting also permits "none", which is useful when the store
// sits behind a filesystem that compresses already, or when a human wants to read a shard
// without tooling.
//
// Reads do not trust the setting. A blob store is long-lived and content-addressed, so it
// can legitimately hold blobs written years apart under different settings; decoding
// therefore sniffs the zstd magic number and falls back to raw bytes. The digest check in
// Store.GetBlob is what makes that sniff safe — a raw blob that happens to begin with the
// magic bytes fails to decode as a frame, falls back to raw, and still verifies. Shards
// are unambiguous because the manifest records each shard's file name, and the extension
// names the codec.
package artifact

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Compression selects the codec for blobs and shards. It mirrors artifacts.compression.
type Compression string

// The supported codecs.
const (
	CompressionZstd Compression = "zstd"
	CompressionNone Compression = "none"
)

// zstdMagic is the four-byte zstd frame header, used to sniff stored content.
var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

// zstdExt is the shard file extension for compressed shards.
const zstdExt = ".zst"

// validate rejects an unknown codec name. An empty value means the default.
func (c Compression) validate() (Compression, error) {
	switch c {
	case "":
		return CompressionZstd, nil
	case CompressionZstd, CompressionNone:
		return c, nil
	default:
		return "", fmt.Errorf("compression %q: want %q or %q", c, CompressionZstd, CompressionNone)
	}
}

// extension returns the suffix appended to a shard file name.
func (c Compression) extension() string {
	if c == CompressionZstd {
		return zstdExt
	}
	return ""
}

// codec holds the reusable encoder and decoder for one store. Both are safe for
// concurrent use by the whole-slice methods, so one pair serves every goroutine.
type codec struct {
	compression Compression
	enc         *zstd.Encoder
	dec         *zstd.Decoder
}

// newCodec builds a codec. maxDecodedBytes bounds a single whole-slice decode, which
// keeps a corrupt or hostile blob from allocating without limit before its digest is
// checked.
func newCodec(compression Compression, maxDecodedBytes int64) (*codec, error) {
	c := &codec{compression: compression}
	// The decoder exists even when writing is uncompressed, since reads sniff.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(uint64(maxDecodedBytes)))
	if err != nil {
		return nil, fmt.Errorf("creating zstd decoder: %w", err)
	}
	c.dec = dec

	if compression == CompressionZstd {
		enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			dec.Close()
			return nil, fmt.Errorf("creating zstd encoder: %w", err)
		}
		c.enc = enc
	}
	return c, nil
}

// close releases the encoder and decoder goroutines.
func (c *codec) close() {
	if c.enc != nil {
		_ = c.enc.Close() // Encoder.Close only flushes a streaming session; nothing to report.
	}
	c.dec.Close()
}

// encode compresses src for storage, or returns it unchanged when compression is off.
func (c *codec) encode(src []byte) []byte {
	if c.enc == nil {
		return src
	}
	return c.enc.EncodeAll(src, make([]byte, 0, len(src)))
}

// decode reverses encode for content whose framing is not known in advance.
func (c *codec) decode(src []byte) ([]byte, error) {
	if !bytes.HasPrefix(src, zstdMagic) {
		return src, nil
	}
	out, err := c.dec.DecodeAll(src, nil)
	if err != nil {
		return nil, fmt.Errorf("decompressing: %w", err)
	}
	return out, nil
}

// newShardWriter wraps w in the codec's streaming encoder. Closing the result flushes the
// frame; it does not close w.
func (c *codec) newShardWriter(w io.Writer) (io.WriteCloser, error) {
	if c.compression != CompressionZstd {
		return nopCloser{w}, nil
	}
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("creating shard encoder: %w", err)
	}
	return enc, nil
}

// newShardReader streams a shard held in memory. The codec comes from the shard's file
// name rather than from configuration, so a store holding shards written under both
// settings reads back correctly.
func newShardReader(path string, data []byte) (io.ReadCloser, error) {
	if !strings.HasSuffix(path, zstdExt) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	dec, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating shard decoder for %s: %w", path, err)
	}
	return dec.IOReadCloser(), nil
}

// nopCloser adapts a plain io.Writer to io.WriteCloser for the uncompressed path.
type nopCloser struct{ io.Writer }

// Close does nothing; the underlying writer is owned by the caller.
func (nopCloser) Close() error { return nil }
