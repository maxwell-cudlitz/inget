// Shared fixtures for the pipeline tests: a sqlite state store, a file:// artifact store, a
// recording generator, a counting embedder and a counting destination.
//
// The counters live in the fakes rather than in the pipeline's own statistics on purpose. An
// assertion that a second run "performs zero LLM calls" is only worth making against the thing
// that would have been called.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/model"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

const (
	testDatatype = "github/repo"
	testSource   = "github"
	testItemID   = "maxwellcudlitz/cascade-test"
	testConfig   = "sha256:test-config"
	numFrags     = 100
	numViews     = 8
	baseVersion  = "v1"
	noChange     = -1
)

// testViews mirrors the github/repo view set from config.yaml: eight views whose globs overlap
// in the ways the cascade has to get right.
func testViews() []config.View {
	return []config.View{
		{Name: "role", Prompt: "role.tmpl", DependsOn: []string{"**"}},
		{Name: "surface", Prompt: "surface.tmpl", DependsOn: []string{"cmd/**", "**/api/**", "README*"}},
		{Name: "internals", Prompt: "internals.tmpl", DependsOn: []string{"**"}},
		{Name: "stack", Prompt: "stack.tmpl", DependsOn: []string{"go.mod", "package.json", "Makefile"}},
		{Name: "integrations", Prompt: "integrations.tmpl", DependsOn: []string{".github/workflows/**", "**/config*"}},
		{Name: "stewardship", Prompt: "stewardship.tmpl", DependsOn: []string{"CODEOWNERS", ".github/**", "docs/**", "README*"}},
		{Name: "operations", Prompt: "operations.tmpl", DependsOn: []string{"Dockerfile*", "Makefile", ".github/workflows/**"}},
		{Name: "aliases", Prompt: "aliases.tmpl", DependsOn: []string{"**"}},
	}
}

// cascadeHarness is one datatype wired to real stores and counting fakes.
type cascadeHarness struct {
	t     *testing.T
	ctx   context.Context
	arts  *artifact.Store
	store state.Store
	gen   *recordingGenerator
	emb   *model.FakeEmbedder
	dest  *testDest
	deps  Deps
	// metadata is merged into every record the harness writes, which is how a reference test
	// gives an item something for key_from to find.
	metadata map[string]string
	// scope is the artifact scope the harness commits with. Empty means full; a test of the
	// self-submission topology sets partial, because a producer that carries one item must
	// not claim the absence of the others means anything.
	scope artifact.Scope
}

// newCascadeHarness builds the harness. State is sqlite in a temp dir and artifacts are
// file://, so the whole cascade runs with no network and no credentials.
func newCascadeHarness(t *testing.T) *cascadeHarness {
	t.Helper()
	dir := t.TempDir()
	ctx := t.Context()

	store := openTestState(t, dir)
	t.Cleanup(func() { _ = store.Close() })
	arts := openTestArtifacts(t, filepath.Join(dir, "artifacts"))
	t.Cleanup(func() { _ = arts.Close() })

	gen := &recordingGenerator{}
	emb := model.NewFakeEmbedder("test-embedder", 64)
	dest := &testDest{}

	views := testViews()
	enrichers := make(map[string]enrich.Enricher, len(views))
	for _, v := range views {
		prompt, err := enrich.ParsePrompt(v.Name, []byte("Generate "+v.Name+": {{.Document}}"))
		if err != nil {
			t.Fatal(err)
		}
		enrichers[v.Name] = enrich.NewLLMEnricher(gen, prompt, enrich.LLMConfig{SchemaVersion: 1})
	}
	fragPrompt, err := enrich.ParsePrompt("fragment", []byte("Summarise {{.Key}}: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}

	return &cascadeHarness{
		t:     t,
		ctx:   ctx,
		arts:  arts,
		store: store,
		gen:   gen,
		emb:   emb,
		dest:  dest,
		deps: Deps{
			State:        store,
			Destinations: map[string]destination.Destination{"test-dest": dest},
			Embedder:     emb,
			Enrichers:    enrichers,
			FragEnricher: enrich.NewFragmentLLMEnricher(gen, fragPrompt, enrich.FragmentEnricherConfig{MaxInputChars: 8000}),
			Config: DatatypeConfig{
				Name:             testDatatype,
				Source:           testSource,
				Destinations:     []string{"test-dest"},
				Views:            views,
				ComposeOrder:     "tier",
				ComposeMaxChars:  120000,
				FragmentEnricher: true,
			},
		},
	}
}

// runConfig is the harness's standard RunConfig. The cascade bounds mirror the shipped
// config.yaml defaults so that a test exercising references sees production behaviour.
func (h *cascadeHarness) runConfig() RunConfig {
	return RunConfig{
		Binary:            "inget",
		Concurrency:       4,
		ConfigHash:        testConfig,
		Pricing:           Pricing{PerMTokIn: 1, PerMTokOut: 2, MaxOutputTokens: 512},
		MaxReferenceDepth: 2,
		MaxCascadePerRun:  5000,
	}
}

// run executes the pipeline.
func (h *cascadeHarness) run() (*Plan, *Stats, error) {
	return Run(h.ctx, h.deps, h.arts, h.runConfig())
}

// plan executes the pipeline in dry-run mode.
func (h *cascadeHarness) plan() (*Plan, error) {
	rc := h.runConfig()
	rc.DryRun = true
	plan, _, err := Run(h.ctx, h.deps, h.arts, rc)
	return plan, err
}

// runInterrupted executes the pipeline with shutdown already signalled, which is what a pod
// that received SIGTERM before it could claim anything looks like.
func (h *cascadeHarness) runInterrupted() (*Plan, *Stats, error) {
	shutdown := make(chan struct{})
	close(shutdown)
	var ch <-chan struct{} = shutdown
	return Run(WithShutdown(h.ctx, ch), h.deps, h.arts, h.runConfig())
}

// runWith executes the pipeline with a caller-modified RunConfig, for the flags the standard
// one does not set.
func (h *cascadeHarness) runWith(rc RunConfig) (*Plan, *Stats, error) {
	return Run(h.ctx, h.deps, h.arts, rc)
}

// reset zeroes every counter between phases.
func (h *cascadeHarness) reset() {
	h.gen.reset()
	h.emb.ResetEmbedded()
	h.dest.reset()
}

// writeRun commits an artifact run holding one item with numFrags fragments. When changedIdx
// is not noChange, that fragment carries changedVersion instead of version.
func (h *cascadeHarness) writeRun(version string, changedIdx int, changedVersion string) {
	h.t.Helper()
	h.writeRunWithItems(version, changedIdx, changedVersion, testItemID)
}

// writeRunWithItems commits an artifact run holding one record per item ID.
func (h *cascadeHarness) writeRunWithItems(version string, changedIdx int, changedVersion string, itemIDs ...string) {
	h.t.Helper()
	scope := h.scope
	if scope == "" {
		scope = artifact.ScopeFull
	}
	w, err := h.arts.NewWriter(h.ctx, artifact.RunInfo{
		Source:     testSource,
		Datatype:   testDatatype,
		Scope:      scope,
		Producer:   "test",
		DomainHash: "sha256:domain",
		ConfigHash: "sha256:config",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, itemID := range itemIDs {
		rec, contents := buildCascadeRecord(itemID, version, changedIdx, changedVersion)
		for k, v := range h.metadata {
			rec.Metadata[k] = v
		}
		for _, content := range contents {
			if _, _, err := h.arts.PutBlob(h.ctx, []byte(content)); err != nil {
				h.t.Fatalf("putting blob: %v", err)
			}
		}
		if err := w.Write(h.ctx, rec); err != nil {
			h.t.Fatal(err)
		}
	}
	if _, err := w.Commit(h.ctx, artifact.CommitInfo{}); err != nil {
		h.t.Fatal(err)
	}
}

// buildCascadeRecord creates a record and the blob contents its fragments refer to. The first
// ten fragments use paths that activate the specific view globs; the rest match only "**".
func buildCascadeRecord(itemID, version string, changedIdx int, changedVersion string) (*artifact.Record, map[string]string) {
	specialKeys := []string{
		"cmd/root.go",              // cmd/**            → surface
		"cmd/api/handler.go",       // cmd/**, **/api/** → surface
		"README.md",                // README*           → surface, stewardship
		"go.mod",                   // go.mod            → stack
		"Makefile",                 // Makefile          → stack, operations
		".github/workflows/ci.yml", // .github/**        → integrations, stewardship, operations
		"Dockerfile",               // Dockerfile*       → operations
		"docs/guide.md",            // docs/**           → stewardship
		"CODEOWNERS",               // CODEOWNERS        → stewardship
		"helm/config.yaml",         // **/config*        → integrations
	}

	frags := make([]artifact.Fragment, numFrags)
	contents := make(map[string]string, numFrags)
	for i := range numFrags {
		key := fmt.Sprintf("src/internal/file_%04d.go", i)
		if i < len(specialKeys) {
			key = specialKeys[i]
		}
		v := version
		if i == changedIdx {
			v = changedVersion
		}
		content := fmt.Sprintf("// %s version %s\npackage internal\n", key, v)
		contents[key] = content
		frags[i] = artifact.Fragment{
			Key:         key,
			Fingerprint: sha256Hex(key + ":" + v),
			Blob:        sha256Hex(content),
			Bytes:       int64(len(content)),
			Tier:        3,
		}
	}

	fingerprint := recordFingerprint(itemID, version, changedIdx, changedVersion)
	return &artifact.Record{
		SchemaVersion: artifact.SchemaVersion,
		Datatype:      testDatatype,
		ItemID:        itemID,
		Fingerprint:   fingerprint,
		FetchedAt:     time.Now(),
		Metadata:      map[string]string{"full_name": itemID},
		Fragments:     frags,
		FragmentCount: numFrags,
	}, contents
}

func openTestState(t *testing.T, dir string) state.Store {
	t.Helper()
	store, err := state.Open(t.Context(), state.Options{
		Driver: state.DriverSQLite,
		Path:   filepath.Join(dir, "state.db"),
	})
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatalf("migrating state: %v", err)
	}
	return store
}

func openTestArtifacts(t *testing.T, dir string) *artifact.Store {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.Open(t.Context(), artifact.Options{
		URL:              "file://" + dir,
		ShardTargetBytes: 1 << 20,
		BlobMaxBytes:     1 << 20,
		Compression:      artifact.CompressionNone,
	})
	if err != nil {
		t.Fatalf("opening artifacts: %v", err)
	}
	return store
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// recordFingerprint is the level-0 fingerprint buildCascadeRecord gives an item. It is exposed
// so a test can seed state that reconciles as unchanged, which is how an item enters a work set
// only because a reference invalidated it.
func recordFingerprint(itemID, version string, changedIdx int, changedVersion string) string {
	return sha256Hex(itemID + ":" + version + ":" + strconv.Itoa(changedIdx) + ":" + changedVersion)
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
