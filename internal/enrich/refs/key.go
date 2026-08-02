// key_from extraction: finding the referent's key inside the referring item.
//
// Two sources, because the two connectors put the same kind of pointer in different places.
// A GitHub repository carries a linked ticket in item metadata; a Monday item carries a
// linked repository in a column, which is a fragment. So "metadata:<field>" reads the
// metadata map and anything else is a glob over fragment keys whose content holds the key.
//
// key_regex then narrows the raw value. Extraction and normalisation are separate on
// purpose: a column holding "https://github.com/acme/thing.git" points at the github/repo
// item "acme/thing", and the alternative to a declared capture group is a resolver that
// knows what a GitHub URL looks like — which is the one thing a generic resolver must not
// know.
package refs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/maxwell-cudlitz/inget/internal/delta"
)

// metadataPrefix marks a key_from that reads item metadata rather than fragment content.
const metadataPrefix = "metadata:"

// keyExtractor locates reference keys in an item. Exactly one of field and glob is set.
type keyExtractor struct {
	field string         // metadata field name
	glob  string         // fragment key pattern
	re    *regexp.Regexp // optional narrowing, one capture group
}

// newKeyExtractor compiles a key_from and key_regex pair.
func newKeyExtractor(keyFrom, keyRegex string) (*keyExtractor, error) {
	if strings.TrimSpace(keyFrom) == "" {
		return nil, fmt.Errorf("key_from is empty")
	}
	k := &keyExtractor{}
	if field, ok := strings.CutPrefix(keyFrom, metadataPrefix); ok {
		k.field = field
	} else {
		k.glob = keyFrom
	}
	if keyRegex == "" {
		return k, nil
	}
	re, err := regexp.Compile(keyRegex)
	if err != nil {
		return nil, fmt.Errorf("key_regex %q: %w", keyRegex, err)
	}
	if n := re.NumSubexp(); n != 1 {
		return nil, fmt.Errorf("key_regex %q has %d capture groups, want exactly 1", keyRegex, n)
	}
	k.re = re
	return k, nil
}

// extract returns the keys this item points at, sorted and deduplicated.
//
// Fragment content is loaded only for the fragments the glob matches, which is what keeps a
// reference from reading every blob of every item. A fragment whose content yields no key is
// skipped rather than failed: a column left empty is a normal state of a board.
func (k *keyExtractor) extract(ctx context.Context, in Input) ([]string, error) {
	if k.field != "" {
		return k.narrow([]string{in.Metadata[k.field]}), nil
	}
	var raw []string
	for _, fragKey := range in.Fragments {
		if !delta.MatchesAny(fragKey, []string{k.glob}) {
			continue
		}
		content, err := in.Content(ctx, fragKey)
		if err != nil {
			return nil, fmt.Errorf("reading fragment %s for a reference key: %w", fragKey, err)
		}
		raw = append(raw, content)
	}
	return k.narrow(raw), nil
}

// narrow applies key_regex, trims, drops empties, and sorts the survivors so that two runs
// over the same item produce the same key order and therefore the same digest.
func (k *keyExtractor) narrow(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	keys := make([]string, 0, len(raw))
	for _, value := range raw {
		key := strings.TrimSpace(value)
		if k.re != nil {
			match := k.re.FindStringSubmatch(value)
			if len(match) < 2 {
				continue
			}
			key = strings.TrimSpace(match[1])
		}
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
