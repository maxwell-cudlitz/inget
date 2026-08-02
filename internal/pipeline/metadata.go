// Stage 8: the metadata a destination row carries.
//
// It lives in its own file, and AnnotateMetadata is exported, because two callers must agree on
// it exactly: the pipeline writing a row for the first time, and `inget reindex` rewriting that
// row from state. A reindex that assembled metadata differently would silently change what a
// metadata filter matches, and the difference would only show up as search results that used to
// be there.
package pipeline

import (
	"strings"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/enrich/refs"
)

// AnnotateMetadata merges an item's own metadata with the datatype's configured
// metadata_fields.
//
// A metadata_fields value naming ${references.*.resolved_keys} is expanded from relatedKeys; the
// design's own example is exactly that, and those keys also reach the destination's typed
// related_keys column, which is what makes them GIN-queryable. Any other "${...}" value is an
// indirection nothing resolves, and is left out rather than written literally so that no
// destination row carries an unexpanded placeholder as if it were data.
func AnnotateMetadata(base, fields map[string]string, relatedKeys []string) map[string]string {
	metadata := make(map[string]string, len(base)+len(fields))
	for k, v := range base {
		metadata[k] = v
	}
	for k, v := range fields {
		switch {
		case !strings.Contains(v, "${"):
			metadata[k] = v
		case isResolvedKeysRef(v):
			if len(relatedKeys) > 0 {
				metadata[k] = strings.Join(relatedKeys, ",")
			}
		}
	}
	return metadata
}

// isResolvedKeysRef reports whether a metadata_fields value asks for the resolved reference
// keys, in either the wildcard or the named form.
func isResolvedKeysRef(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "${references.") &&
		strings.HasSuffix(trimmed, ".resolved_keys}")
}

// annotateMetadata is the pipeline's caller: the record's own metadata, the values any
// metadata-injected reference resolved to, then the configured fields.
//
// The reference-injected values are the one part of this a reindex cannot reproduce, because
// they are not persisted; see internal/reindex.
func annotateMetadata(cfg DatatypeConfig, rec *artifact.Record, resolved refs.Resolution) map[string]string {
	base := make(map[string]string, len(rec.Metadata)+len(resolved.Metadata))
	for k, v := range rec.Metadata {
		base[k] = v
	}
	for k, v := range resolved.Metadata {
		base[k] = v
	}
	return AnnotateMetadata(base, cfg.MetadataFields, resolved.RelatedKeys)
}
