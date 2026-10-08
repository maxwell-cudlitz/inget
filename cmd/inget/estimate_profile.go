// Optional cost-calibration input for planning.
//
// Profiles contain measured token/character relationships and enricher signatures,
// never credentials or executable instructions. They change reporting only; the
// pipeline verifies that their observations match the configured enrichment flows.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/maxwell-cudlitz/inget/internal/pipeline"
)

// loadEstimateProfile decodes exactly one profile, refusing unknown fields and trailing data.
func loadEstimateProfile(path string) (*pipeline.EstimateProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening estimate profile %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	const maxProfileBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading estimate profile %s: %w", path, err)
	}
	if len(data) > maxProfileBytes {
		return nil, fmt.Errorf("estimate profile %s exceeds 1 MiB", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var profile pipeline.EstimateProfile
	if err := decoder.Decode(&profile); err != nil {
		return nil, fmt.Errorf("decoding estimate profile %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("reading trailing estimate profile data in %s: %w", path, err)
		}
		return nil, fmt.Errorf("estimate profile %s contains multiple JSON values", path)
	}
	return &profile, nil
}
