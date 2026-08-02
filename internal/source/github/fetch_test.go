// Tests for Fetch: the whole path from repository metadata through the tree and the tarball into
// fragments, with the filters, tiers, splits, secret exclusions and warnings that path applies.
package github

import (
	"slices"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/source"
)

// serviceFiles is a repository shaped like a real one: some content worth indexing, and a
// representative sample of the noise that must not be.
func serviceFiles() map[string]string {
	return map[string]string{
		"README.md":                    "# svc\n\nWhat the service does.\n",
		"docs/design.md":               "# design\n\nHow it works.\n",
		"cmd/svc/main.go":              "package main\n\nfunc main() {}\n",
		"go.mod":                       "module example.com/svc\n\ngo 1.26\n",
		"internal/store/store.go":      "package store\n\ntype Store struct{}\n",
		"go.sum":                       "example.com/dep v1.0.0 h1:abc=\n",
		"node_modules/dep/index.js":    "module.exports = {}\n",
		"internal/store/store_test.go": "package store\n",
		"assets/logo.png":              "\x89PNG\r\n\x1a\n binary",
		"web/app.min.js":               "!function(){}();\n",
	}
}

// fetchOne fetches a repository from the fake and fails the test if it errors.
func fetchOne(t *testing.T, conn *Connector, id string) source.Result {
	t.Helper()
	result, err := conn.Fetch(t.Context(), Datatype, source.Ref{ID: id})
	if err != nil {
		t.Fatalf("Fetch(%s) = %v", id, err)
	}
	return result
}

func TestFetchKeepsOnlySignalPaths(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{
		Name: "svc", Description: "a service", Language: "Go",
		Topics: []string{"rag", "golang"},
	}, serviceFiles())

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")

	want := []string{
		"README.md", "docs/design.md", // tier 0
		"cmd/svc/main.go",         // tier 1
		"go.mod",                  // tier 2
		"internal/store/store.go", // tier 3
	}
	if got := fragmentKeys(result.Fragments); !slices.Equal(got, want) {
		t.Errorf("fragments = %v, want %v in tier then path order", got, want)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for a clean repository", result.Warnings)
	}
}

// Ordering is load-bearing twice over: the composed hash depends on it, and a
// max_fragments_per_item cap is applied as a prefix, so tier order is what decides that a capped
// repository keeps its documentation rather than its alphabetically-first source file.
func TestFetchOrdersFragmentsByTierThenPath(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, serviceFiles())

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")
	for i, frag := range result.Fragments {
		if i == 0 {
			continue
		}
		previous := result.Fragments[i-1]
		if frag.Tier < previous.Tier {
			t.Fatalf("fragment %d (%s, tier %d) follows tier %d", i, frag.Key, frag.Tier, previous.Tier)
		}
		if frag.Tier == previous.Tier && frag.Key < previous.Key {
			t.Fatalf("fragments %q and %q are out of path order within tier %d", previous.Key, frag.Key, frag.Tier)
		}
	}
}

// The tree endpoint's blob SHA is the level-1 fingerprint. It has to be the real git hash, because
// that is what makes an unchanged file detectable without transferring it.
func TestFetchFingerprintsFragmentsWithGitBlobSHAs(t *testing.T) {
	files := serviceFiles()
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, files)

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")
	readme := findFragment(t, result.Fragments, "README.md")
	if readme.Fingerprint != gitBlobSHA(files["README.md"]) {
		t.Errorf("fingerprint = %q, want the git blob SHA of the content", readme.Fingerprint)
	}
	if string(readme.Content) != files["README.md"] {
		t.Errorf("content = %q, want the file's bytes", readme.Content)
	}
	if readme.Bytes != int64(len(files["README.md"])) {
		t.Errorf("bytes = %d, want %d", readme.Bytes, len(files["README.md"]))
	}
}

func TestFetchCarriesItemMetadata(t *testing.T) {
	f := newFakeGitHub(t)
	fix := f.addRepo("acme", repo{
		Name: "svc", Description: "a service", Language: "Go",
		Topics: []string{"rag", "golang"}, HTMLURL: "https://github.com/acme/svc",
	}, serviceFiles())

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")
	want := map[string]string{
		"name":           "svc",
		"full_name":      "acme/svc",
		"description":    "a service",
		"language":       "Go",
		"topics":         "rag,golang",
		"url":            "https://github.com/acme/svc",
		"default_branch": "main",
		"visibility":     VisibilityPublic,
	}
	for key, value := range want {
		if got := result.Item.Metadata[key]; got != value {
			t.Errorf("metadata[%q] = %q, want %q", key, got, value)
		}
	}
	if result.Item.Fingerprint != fix.meta.fingerprint() {
		t.Errorf("item fingerprint = %q, want the pushed_at token %q", result.Item.Fingerprint, fix.meta.fingerprint())
	}
}

// One repository costs one metadata request, one tree request and one archive. Anything more means
// the connector is spending quota per file, which is the thing the tree endpoint exists to avoid.
func TestFetchSpendsThreeRequestsPerRepository(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, serviceFiles())

	fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")

	for substr, want := range map[string]int{
		"/repos/acme/svc/git/trees/": 1,
		"/repos/acme/svc/tarball/":   1,
	} {
		if got := f.countPaths(substr); got != want {
			t.Errorf("%s requested %d times, want %d", substr, got, want)
		}
	}
	if got := len(f.paths()); got != 3 {
		t.Errorf("served %d requests for one repository, want 3: %v", got, f.paths())
	}
}

func TestFetchSplitsOversizedFiles(t *testing.T) {
	files := serviceFiles()
	files["docs/reference.md"] = strings.Repeat("a documentation line that goes on\n", 100)

	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, files)
	conn := f.connector(orgDomain("acme"), nil)
	conn.fragmentMaxBytes = 512

	result := fetchOne(t, conn, "acme/svc")
	var parts []string
	for _, frag := range result.Fragments {
		if strings.HasPrefix(frag.Key, "docs/reference.md#") {
			parts = append(parts, frag.Key)
		}
		if frag.Key == "docs/reference.md" {
			t.Error("the oversized file is still one fragment, so it was not split")
		}
	}
	if len(parts) < 2 {
		t.Fatalf("oversized file produced %v, want several numbered pieces", parts)
	}
	if parts[0] != "docs/reference.md#0" {
		t.Errorf("first piece = %q, want a #0 suffix", parts[0])
	}
}

// A file too large to be worth transferring is still recorded: its presence and its fingerprint
// describe the repository, and the consumer can tell there is content it did not get.
func TestFetchRecordsFilesOverTheSplitBudgetWithoutContent(t *testing.T) {
	files := serviceFiles()
	files["data/dump.sql"] = strings.Repeat("INSERT INTO t VALUES (1);\n", 2000)

	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, files)
	conn := f.connector(orgDomain("acme"), nil)
	conn.fragmentMaxBytes = 512 // budget is 512 * maxParts = 8 KiB

	result := fetchOne(t, conn, "acme/svc")
	frag := findFragment(t, result.Fragments, "data/dump.sql")
	if len(frag.Content) != 0 {
		t.Error("a file over the split budget carried content, which the budget exists to prevent")
	}
	if !frag.Truncated {
		t.Error("a file recorded without content is not marked truncated")
	}
	if frag.Bytes != int64(len(files["data/dump.sql"])) {
		t.Errorf("bytes = %d, want the size at the source %d", frag.Bytes, len(files["data/dump.sql"]))
	}
	if !hasWarningContaining(result.Warnings, "without content") {
		t.Errorf("warnings = %v, want one naming the file recorded without content", result.Warnings)
	}
}

func TestFetchExcludesContentWithDetectedSecrets(t *testing.T) {
	// Assembled rather than written out, so this test file does not itself read as a leak.
	token := "ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a"
	files := serviceFiles()
	files["internal/store/client.go"] = "package store\n\nconst token = \"" + token + "\"\n"

	f := newFakeGitHub(t)
	f.addRepo("acme", repo{Name: "svc"}, files)

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/svc")
	frag := findFragment(t, result.Fragments, "internal/store/client.go")
	if len(frag.Content) != 0 {
		t.Fatal("content with a detected secret was carried into the artifact")
	}
	if frag.Meta["excluded"] != "secret" {
		t.Errorf("meta = %v, want excluded=secret so the exclusion is recorded", frag.Meta)
	}
	if frag.Meta["rules"] == "" {
		t.Error("meta records no rule, so an operator cannot tell what matched")
	}
	if frag.Fingerprint != gitBlobSHA(files["internal/store/client.go"]) {
		t.Error("an excluded fragment lost its fingerprint, so its next change would be invisible")
	}
	if !hasWarningContaining(result.Warnings, "detected secrets") {
		t.Errorf("warnings = %v, want one naming the exclusion", result.Warnings)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, token) {
			t.Fatal("a warning contains the detected secret")
		}
	}
	// Every other fragment must be unaffected: one bad file is not a reason to drop a repository.
	if len(result.Fragments) != 6 {
		t.Errorf("fragments = %v, want the five clean paths plus the excluded one", fragmentKeys(result.Fragments))
	}
}

func TestFetchWarnsWhenTheTreeIsTruncated(t *testing.T) {
	f := newFakeGitHub(t)
	fix := f.addRepo("acme", repo{Name: "monorepo"}, serviceFiles())
	fix.treeTruncated = true

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/monorepo")
	if !hasWarningContaining(result.Warnings, "truncated") {
		t.Errorf("warnings = %v, want one reporting the truncated tree", result.Warnings)
	}
	if len(result.Fragments) == 0 {
		t.Error("a truncated tree produced no fragments; a partial view is still worth indexing")
	}
}

// An empty repository is a valid item with nothing in it. Failing here would fail a whole run over
// a repository somebody created and never pushed to.
func TestFetchToleratesAnEmptyRepository(t *testing.T) {
	f := newFakeGitHub(t)
	fix := f.addRepo("acme", repo{Name: "fresh"}, nil)
	fix.meta.DefaultBranch = ""

	result := fetchOne(t, f.connector(orgDomain("acme"), nil), "acme/fresh")
	if len(result.Fragments) != 0 {
		t.Errorf("fragments = %v, want none", fragmentKeys(result.Fragments))
	}
	if len(result.Warnings) == 0 {
		t.Error("an empty repository produced no warning, so the run looks like it indexed one")
	}
}

func TestFetchRejectsAnUnknownDatatype(t *testing.T) {
	f := newFakeGitHub(t)
	if _, err := f.connector(orgDomain("acme"), nil).Fetch(t.Context(), "monday/item", source.Ref{ID: "x"}); err == nil {
		t.Fatal("Fetch() with a foreign datatype = nil error, want failure")
	}
}

func TestItemIDsFromEvent(t *testing.T) {
	f := newFakeGitHub(t)
	conn := f.connector(orgDomain("acme"), nil)

	tests := []struct {
		name    string
		payload string
		want    []string
		wantErr bool
	}{
		{"push event", `{"ref":"refs/heads/main","repository":{"full_name":"acme/svc"}}`, []string{"acme/svc"}, false},
		{"installation event", `{"repositories":[{"full_name":"acme/a"},{"full_name":"acme/b"}]}`, []string{"acme/a", "acme/b"}, false},
		{"no repository", `{"zen":"keep it logically awesome"}`, nil, true},
		{"not json", `nope`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := conn.ItemIDsFromEvent([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ItemIDsFromEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !slices.Equal(got, tt.want) {
				t.Errorf("ItemIDsFromEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The connector must satisfy both interfaces it claims, or the CLI's type assertion silently stops
// mapping webhook payloads.
func TestConnectorSatisfiesItsInterfaces(t *testing.T) {
	var _ source.Connector = (*Connector)(nil)
	var _ source.EventMapper = (*Connector)(nil)
}

func hasWarningContaining(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}
	return false
}
