// Secret detection over candidate content, before it can reach a blob store or a prompt.
//
// Repository tarballs contain credentials. The noise filter already drops the obvious carriers
// by path — .pem, .p12, keystores — but a hard-coded token in a source file has no distinguishing
// path, so content has to be examined. Content that matches is excluded from the artifact
// entirely and the exclusion is recorded, so an operator can see what was dropped and why
// without the artifact containing the thing that was dropped.
//
// The ruleset is gitleaks' own (D15) rather than a bespoke pattern list: it is roughly 800
// maintained rules with entropy and allowlist handling, and a hand-rolled alternative would be a
// worse version of the same thing that nobody updates. It is pure Go — its regex engine runs on
// wazero rather than through CGO — so binaries still cross-compile statically.
//
// Redaction is set to full. A finding therefore carries no plaintext secret, so a detected
// credential never enters this process's memory beyond the file content that is about to be
// discarded, and cannot reach a log line by accident.
package github

import (
	"fmt"
	"sort"

	"github.com/zricethezav/gitleaks/v8/detect"
)

// redactFully is gitleaks' redaction percentage. 100 replaces the whole matched secret.
const redactFully = 100

// scanner detects secrets in content. A zero scanner — what a disabled configuration produces —
// finds nothing, so no call site needs a nil check.
//
// The detector is built once because construction compiles the entire ruleset. It is safe for
// concurrent use: after construction it is read-only, and DetectString accumulates nothing.
type scanner struct {
	detector *detect.Detector
}

// newScanner builds a scanner, or a disabled one when enabled is false.
func newScanner(enabled bool) (*scanner, error) {
	if !enabled {
		return &scanner{}, nil
	}
	detector, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return nil, fmt.Errorf("loading the gitleaks default ruleset: %w", err)
	}
	detector.Redact = redactFully
	detector.Verbose = false
	detector.NoColor = true
	return &scanner{detector: detector}, nil
}

// rules returns the sorted, deduplicated IDs of the rules that matched content, or nil when
// nothing did. Rule IDs are safe to record and to log; the secret values are not returned at all.
func (s *scanner) rules(content []byte) []string {
	if s == nil || s.detector == nil || len(content) == 0 {
		return nil
	}
	findings := s.detector.DetectBytes(content)
	if len(findings) == 0 {
		return nil
	}
	unique := make(map[string]bool, len(findings))
	for _, finding := range findings {
		id := finding.RuleID
		if id == "" {
			id = "unknown"
		}
		unique[id] = true
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// enabled reports whether detection is on, for the log line that says so once per run.
func (s *scanner) enabled() bool { return s != nil && s.detector != nil }
