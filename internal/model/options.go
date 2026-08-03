// Provider request options: the fields a specific model needs and this client does not know
// about.
//
// Every provider behind an OpenAI-compatible endpoint adds its own knobs — DeepSeek's
// `thinking`, a reasoning effort, a top_k — and hard-coding each one would make the client a
// list of vendors. So configuration carries an opaque map that is merged into the request body,
// which is the generic version of the question: any provider-specific parameter works without
// new code, and the signature covers it, because a parameter that changes output must invalidate
// what was cached under the old one (D2).
//
// Reserved keys are refused rather than overridden. A `max_tokens` supplied here would silently
// replace the budget the truncation guards are written against, and a `messages` would replace
// the prompt the cache key claims was sent.
package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// reservedRequestKeys are set by this client from typed configuration, so an option map may not
// carry them.
var reservedRequestKeys = []string{"messages", "max_tokens", "model", "seed", "stream", "temperature"}

// ValidateRequestOptions reports whether an option map may be sent, naming every reserved key it
// carries. It is exported so configuration can reject the map at load time rather than on the
// first call.
func ValidateRequestOptions(options map[string]any) error {
	var clashes []string
	for key := range options {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("request option with an empty name")
		}
		if slices.Contains(reservedRequestKeys, key) {
			clashes = append(clashes, key)
		}
	}
	if len(clashes) == 0 {
		return nil
	}
	sort.Strings(clashes)
	return fmt.Errorf("request options may not set %s: this client sets them from typed configuration",
		strings.Join(clashes, ", "))
}

// encodeRequest marshals a chat request with the configured options merged in.
//
// The merge goes through a map rather than a second struct because the whole point is that the
// extra fields are unknown at compile time. Typed fields are written first and the options
// second, but a reserved key cannot reach here: configuration refused it at load time and this
// function refuses it again, because the client is also constructed directly in tests.
func encodeRequest(body chatRequest, options map[string]any) ([]byte, error) {
	if len(options) == 0 {
		return json.Marshal(body)
	}
	if err := ValidateRequestOptions(options); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var merged map[string]any
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, value := range options {
		merged[key] = value
	}
	return json.Marshal(merged)
}

// renderRequestOptions returns a stable string for the signature. encoding/json sorts map keys,
// so marshalling is deterministic and two configurations differing only in option order produce
// the same signature — which is correct, since they produce the same request.
func renderRequestOptions(options map[string]any) string {
	if len(options) == 0 {
		return ""
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		// Unmarshalable options cannot have been sent, so any stable marker will do; the
		// request itself fails with the real error.
		return "unmarshalable"
	}
	return string(encoded)
}
