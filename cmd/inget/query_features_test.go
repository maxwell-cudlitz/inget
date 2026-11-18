// Feature discovery is a no-config/no-secret API for optional native consumers.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/cli"
)

func TestQueryFeaturesIgnoresMalformedConfigurationAndMissingSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := cli.NewRoot(cli.App{Name: "inget"})
	root.AddCommand(queryFeaturesCommand())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--config", path, "query-features", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("feature discovery read configuration: %v", err)
	}
	var report queryFeatureReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.SchemaVersion != 1 || report.MaxCandidateLimit != 1000 || len(report.Features) != 3 {
		t.Fatalf("feature report=%s err=%v", out.Bytes(), err)
	}
}
