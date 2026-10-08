// Estimate-profile loading tests enforce a strict, data-only document boundary.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEstimateProfile(t *testing.T) {
	tests := []struct {
		name string
		data string
		wantError bool
	}{
		{"valid", `{"version":1,"datatype":"github/repo","views":{}}`, false},
		{"unknown field", `{"version":1,"execute":"anything"}`, true},
		{"trailing object", `{"version":1} {"version":2}`, true},
		{"trailing garbage", `{"version":1} garbage`, true},
		{"malformed", `{`, true},
		{"empty", ``, true},
		{"oversized", `{"version":1}` + strings.Repeat(" ", 1<<20), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.json")
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			profile, err := loadEstimateProfile(path)
			if (err != nil) != tt.wantError {
				t.Fatalf("loadEstimateProfile() error = %v, wantError %v", err, tt.wantError)
			}
			if !tt.wantError && (profile.Version != 1 || profile.Datatype != "github/repo") {
				t.Fatalf("wrong profile: %+v", profile)
			}
		})
	}
}
