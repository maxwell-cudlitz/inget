// The http resolver: references into a system inget does not ingest.
//
// Three bounds make an arbitrary external endpoint safe to put in front of a prompt. The
// endpoint template is expanded once, at construction, so a missing environment variable
// fails at startup rather than per item. The key is escaped into the path, so a key extracted
// from third-party content cannot add path segments or query parameters. And the declared
// fields are an allowlist applied to the decoded response, so an endpoint that grows a field
// does not silently start feeding it to a model — a response is untrusted input, and the
// prompt only ever sees values config asked for.
package refs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// keyPlaceholder is substituted with the resolved key in the endpoint template.
	keyPlaceholder = "{key}"

	// httpTimeout bounds one reference lookup. A reference is one small read on the item's
	// critical path, so it gets a short timeout rather than the generator's.
	httpTimeout = 15 * time.Second

	// maxResponseBytes bounds what is decoded from an endpoint nobody here controls.
	maxResponseBytes = 1 << 20
)

func init() { Register(KindHTTP, newHTTPResolver) }

// httpResolver reads one JSON document per key from an external endpoint.
type httpResolver struct {
	name     string
	endpoint string
	token    string
	client   *http.Client
}

// newHTTPResolver builds the resolver, expanding ${VAR} in the endpoint template.
func newHTTPResolver(opts Options) (Resolver, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("resolver %s needs an endpoint", KindHTTP)
	}
	endpoint, err := expandEnv(opts.Endpoint)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(endpoint, keyPlaceholder) {
		return nil, fmt.Errorf("endpoint %q has no %s placeholder, so every key would read the same record",
			opts.Endpoint, keyPlaceholder)
	}
	// Parsed with the placeholder still in it: {key} is legal in a path segment, and this is
	// the last chance to reject a template that is not a URL at all.
	if u, err := url.Parse(endpoint); err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("endpoint %q is not an absolute URL", endpoint)
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	return &httpResolver{name: opts.Name, endpoint: endpoint, token: opts.Token, client: client}, nil
}

// expandEnv replaces ${VAR} with the environment's value, failing on an unset variable.
//
// os.Expand cannot report a miss, so the lookup records one. An unset variable expanding to
// "" would produce a URL that is syntactically fine and points somewhere else entirely.
func expandEnv(template string) (string, error) {
	var missing []string
	expanded := os.Expand(template, func(name string) string {
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("endpoint %q needs environment variables that are not set: %s",
			template, strings.Join(missing, ", "))
	}
	return expanded, nil
}

// Kind implements Resolver.
func (r *httpResolver) Kind() string { return KindHTTP }

// Resolve implements Resolver. A 404 is an unresolved record rather than an error, matching
// the inget resolver: a referent that does not exist is a fact about the data, not a failure.
func (r *httpResolver) Resolve(ctx context.Context, key string, fields []string) (Record, error) {
	target := strings.ReplaceAll(r.endpoint, keyPlaceholder, url.PathEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Record{}, fmt.Errorf("building request for reference %s: %w", r.name, err)
	}
	req.Header.Set("Accept", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return Record{}, fmt.Errorf("resolving reference %s for key %s: %w", r.name, key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return Record{Key: key}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Record{}, fmt.Errorf("resolving reference %s for key %s: HTTP %d", r.name, key, resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return Record{}, fmt.Errorf("decoding reference %s for key %s: %w", r.name, key, err)
	}
	return Record{Key: key, Fields: allowlist(payload, fields)}, nil
}

// allowlist keeps only the declared fields, rendering each as text. A nested object or array
// is dropped rather than serialised: config asked for a field to put in front of a model, and
// a JSON blob is not that.
func allowlist(payload map[string]any, fields []string) map[string]string {
	kept := make(map[string]string, len(fields))
	for _, field := range fields {
		value, ok := payload[field]
		if !ok {
			continue
		}
		if text, ok := scalar(value); ok {
			kept[field] = text
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// scalar renders a JSON scalar as text, reporting false for anything else.
func scalar(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, v != ""
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		return "", false
	}
}
