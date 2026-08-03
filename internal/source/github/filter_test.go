// Tests for the two decisions that govern what a repository costs to index: which paths become
// fragments, and in what order a composer reads them.
//
// The cases are chosen to pin behaviour that is easy to break by editing a pattern list: a
// separator-free pattern must match at any depth, a directory pattern must match at the root, and
// a path matching no tier group must still be kept rather than dropped.
package github

import (
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/source"
)

func TestKeepPath(t *testing.T) {
	tests := []struct {
		path string
		keep bool
	}{
		// Kept: the content a reader would actually read.
		{"README.md", true},
		{"cmd/inget/main.go", true},
		{"internal/delta/reconcile.go", true},
		{"go.mod", true},
		{"Makefile", true},
		{".github/workflows/ci.yaml", true},
		{"deploy/docker-compose.yaml", true},
		{"docs/feature-design.md", true},
		{"terraform/main.tf", true},
		{"unfamiliar.q", true}, // an extension no list knows is still probably code

		// Dropped: content that is not text.
		{"assets/logo.png", false},
		{"docs/diagram.svg", false},
		{"fonts/Inter.woff2", false},
		{"bin/inget", false},
		{"certs/server.pem", false},

		// Dropped: code this repository did not write, at the root and nested.
		{"node_modules/lodash/index.js", false},
		{"web/node_modules/react/index.js", false},
		{"vendor/github.com/spf13/cobra/command.go", false},
		{"target/debug/build.rs", false},
		{".terraform/providers/registry.tf", false},

		// Dropped: generated output.
		{"api/service.pb.go", false},
		{"web/app.min.js", false},
		{"web/app.min.css", false},
		{"web/app.js.map", false},
		{"src/third_party/lighthouse-bundle.js", false},
		{"web/vendor.bundle.js", false},
		{"apis/zz_generated.deepcopy.go", false},

		// Dropped: tests and fixtures.
		{"internal/state/store_test.go", false},
		{"internal/config/testdata/config.yaml", false},
		{"tests/e2e/login.py", false},
		{"web/src/App.test.tsx", false},
		{"pkg/mocks/client.go", false},

		// Dropped: execution dumps, datasets and serialized models, which are records of a
		// run or a corpus rather than a description of the program.
		{"java_pid26365.hprof", false},
		{"profiles/cpu.pprof", false},
		{"logs/server.log", false},
		{"benchmarks/results.csv", false},
		{"models/encoder.safetensors", false},
		{"traces/capture.pcap", false},
		// A package named data is still source: only shape, not the directory name, drops it.
		{"internal/data/loader.go", true},

		// Dropped: lockfiles and boilerplate.
		{"package-lock.json", false},
		{"go.sum", false},
		{"web/yarn.lock", false},
		{"LICENSE", false},
		{"docs/CHANGELOG.md", false},
		{".gitignore", false},
		{".editorconfig", false},
		{"web/.npmrc", false},

		// Dropped: nothing to address.
		{"", false},
		{"../etc/passwd", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := keepPath(tt.path); got != tt.keep {
				t.Errorf("keepPath(%q) = %v, want %v", tt.path, got, tt.keep)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		tier int
	}{
		{"README.md", source.TierDocs},
		{"docs/feature-design.md", source.TierDocs},
		{"AGENTS.md", source.TierDocs},
		{"internal/notes.rst", source.TierDocs},
		// A directory named doc is a Go or Python package as often as it is documentation, so
		// the extension decides: this is source that happens to live there.
		{"doc/man_docs.go", source.TierSource},
		{"doc/architecture.md", source.TierDocs},

		{"cmd/inget/main.go", source.TierEntrypoints},
		{"cmd/inget/run.go", source.TierEntrypoints},
		{"web/src/index.tsx", source.TierEntrypoints},
		{"app/__main__.py", source.TierEntrypoints},

		{"go.mod", source.TierConfig},
		{"package.json", source.TierConfig},
		{"Makefile", source.TierConfig},
		{"Dockerfile", source.TierConfig},
		{"deploy/docker-compose.yaml", source.TierConfig},
		{".github/workflows/ci.yaml", source.TierConfig},
		{"infra/main.tf", source.TierConfig},
		{"api/service.proto", source.TierConfig},

		{"internal/delta/reconcile.go", source.TierSource},
		{"src/app/service.ts", source.TierSource},
		{"lib/parser.rs", source.TierSource},
		{"web/styles/main.css", source.TierSource},

		{"data/sample.q", source.TierOther},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := classify(tt.path); got != tt.tier {
				t.Errorf("classify(%q) = %d, want %d", tt.path, got, tt.tier)
			}
		})
	}
}

// Tier order is the envelope's contract with the composer, so the constants must stay in the
// documented sequence even if the pattern lists are rearranged.
func TestTiersAreOrderedByInformationDensity(t *testing.T) {
	ordered := []int{source.TierDocs, source.TierEntrypoints, source.TierConfig, source.TierSource, source.TierOther}
	for i := range ordered {
		if ordered[i] != i {
			t.Fatalf("tier constants = %v, want 0..4 in order", ordered)
		}
	}
}

func TestMIMEType(t *testing.T) {
	tests := []struct{ path, want string }{
		{"main.go", "text/x-go"},
		{"README.md", "text/markdown"},
		{"infra/main.tf", "text/x-terraform"},
		{"config.yaml", "application/yaml"},
		{"index.html", "text/html"},
		{"Makefile", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := mimeType(tt.path); got != tt.want {
				t.Errorf("mimeType(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
