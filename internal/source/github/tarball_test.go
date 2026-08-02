// Tests for archive extraction: the wrapper directory is stripped, the want predicate is honoured,
// and every bound truncates rather than fails.
//
// Truncating rather than failing is the behaviour worth pinning. A repository that overruns a bound
// should be indexed as far as it was read, with the manifest saying so; failing the run would mean
// indexing nothing because one repository was large.
package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// tarGz builds a gzipped tar archive wrapped in GitHub's owner-repo-sha directory, which is what
// the tarball endpoint returns.
func tarGz(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		header := &tar.Header{
			Name:     root + "/" + path,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(content)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func keepAll(string) bool { return true }

func TestExtractStripsTheWrapperDirectory(t *testing.T) {
	raw := tarGz(t, "maxwellcudlitz-inget-9f2c1ab", map[string]string{
		"README.md":         "# inget\n",
		"cmd/inget/main.go": "package main\n",
	})

	got, err := extract(bytes.NewReader(raw), 1<<20, 1<<20, keepAll)
	if err != nil {
		t.Fatalf("extract() = %v", err)
	}
	if got.truncated {
		t.Error("extract() reported truncation on an archive that fits")
	}
	for _, want := range []string{"README.md", "cmd/inget/main.go"} {
		if _, ok := got.files[want]; !ok {
			t.Errorf("extract() missing %q; got %v", want, keys(got.files))
		}
	}
}

func TestExtractHonoursTheWantPredicate(t *testing.T) {
	raw := tarGz(t, "owner-repo-sha", map[string]string{
		"README.md":               "# docs\n",
		"node_modules/lodash.js":  "module.exports = 1\n",
		"internal/delta/delta.go": "package delta\n",
	})

	got, err := extract(bytes.NewReader(raw), 1<<20, 1<<20, func(p string) bool {
		return keepPath(p)
	})
	if err != nil {
		t.Fatalf("extract() = %v", err)
	}
	if _, ok := got.files["node_modules/lodash.js"]; ok {
		t.Error("extract() retained a filtered path, so the filter is not saving any memory")
	}
	if len(got.files) != 2 {
		t.Errorf("extract() kept %v, want the two source paths", keys(got.files))
	}
}

func TestExtractSkipsFilesOverThePerFileBound(t *testing.T) {
	raw := tarGz(t, "owner-repo-sha", map[string]string{
		"small.txt": "tiny\n",
		"huge.txt":  strings.Repeat("x", 4096),
	})

	got, err := extract(bytes.NewReader(raw), 1<<20, 1024, keepAll)
	if err != nil {
		t.Fatalf("extract() = %v", err)
	}
	if _, ok := got.files["huge.txt"]; ok {
		t.Error("extract() retained a file over the per-file bound")
	}
	if _, ok := got.files["small.txt"]; !ok {
		t.Error("extract() dropped a file that fits alongside one that does not")
	}
}

func TestExtractTruncatesAtTheTotalBound(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		files[name] = strings.Repeat("y", 512)
	}
	raw := tarGz(t, "owner-repo-sha", files)

	got, err := extract(bytes.NewReader(raw), 1024, 1024, keepAll)
	if err != nil {
		t.Fatalf("extract() = %v", err)
	}
	if !got.truncated {
		t.Error("extract() did not report truncation after exceeding the total bound")
	}
	if len(got.files) > 2 {
		t.Errorf("extract() kept %d files under a 2-file budget", len(got.files))
	}
}

// A transfer cut off mid-stream — which is what the compressed bound does — is truncation, not
// corruption. Reporting it as an error would fail a run over a repository that is merely large.
func TestExtractTreatsATruncatedStreamAsTruncation(t *testing.T) {
	raw := tarGz(t, "owner-repo-sha", map[string]string{
		"a.txt": strings.Repeat("z", 4096),
		"b.txt": strings.Repeat("w", 4096),
	})

	got, err := extract(bytes.NewReader(raw[:len(raw)/2]), 1<<20, 1<<20, keepAll)
	if err != nil {
		t.Fatalf("extract() on a cut-off stream = %v, want truncation rather than an error", err)
	}
	if !got.truncated {
		t.Error("extract() did not report truncation on a cut-off stream")
	}
}

func TestExtractRejectsNonArchiveInput(t *testing.T) {
	if _, err := extract(strings.NewReader("this is not gzip"), 1<<20, 1<<20, keepAll); err == nil {
		t.Error("extract() on non-archive input = nil error, want failure")
	}
}

func TestStripRoot(t *testing.T) {
	tests := []struct{ in, want string }{
		{"owner-repo-sha/README.md", "README.md"},
		{"owner-repo-sha/cmd/main.go", "cmd/main.go"},
		{"./owner-repo-sha/go.mod", "go.mod"},
		{"owner-repo-sha", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := stripRoot(tt.in); got != tt.want {
			t.Errorf("stripRoot(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
