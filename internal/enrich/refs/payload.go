// How a resolved payload reaches a prompt, and how it is addressed in the reverse index.
//
// Rendering is deterministic to the byte because the level-2 guard hashes the composed
// document: a field order that varied with map iteration would read as a changed input and
// pay for a regeneration on every run.
package refs

import (
	"fmt"
	"strings"
)

// edgeKey renders the reverse-index key of one resolved reference. It is namespaced by the
// referenced datatype for inget and by the reference name for http, so the reverse lookup a
// cascade performs cannot collide two unrelated referents under one raw key.
func (r reference) edgeKey(key string) string {
	if r.kind == KindInget {
		return IngetKey(r.datatype, key)
	}
	return r.name + "|" + key
}

// IngetKey renders the reverse-index key of one persisted record. Resolution and the cascade
// both go through it, so the key a reference writes is the key an invalidation looks up.
//
// The separator is a pipe because neither a datatype name nor an item ID contains one, while
// both contain slashes: "github/repo" and "acme/thing" would concatenate ambiguously.
func IngetKey(datatype, itemID string) string {
	return datatype + "|" + itemID
}

// render lays out a reference's records for composition.
//
// The payload deliberately has no envelope of its own. It becomes a compose entry, and the
// composed document is already fenced as untrusted data by the view prompts, so a second
// framing here would only give an injected instruction a second place to look plausible.
func (r reference) render(records []Record) string {
	var b strings.Builder
	for i, rec := range records {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s: %s\n", r.name, rec.Key)
		for _, field := range sortedKeys(rec.Fields) {
			fmt.Fprintf(&b, "%s: %s\n", field, rec.Fields[field])
		}
	}
	return b.String()
}

// injectMetadata writes a reference's fields into item metadata under "<name>.<field>", which
// keeps two references pulling the same field name apart.
func (r reference) injectMetadata(metadata map[string]string, records []Record) {
	for _, rec := range records {
		for field, value := range rec.Fields {
			metadata[r.name+"."+field] = value
		}
	}
}
