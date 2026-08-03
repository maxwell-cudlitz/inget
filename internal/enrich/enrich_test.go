// Tests for the enrichers and prompt loading.
//
// Both enrichers are covered — passthrough returning its input untouched, llm calling the
// generator — along with the property that a prompt byte change moves the enricher
// signature, which is what makes a prompt edit regenerate the views it affects. Output
// validation is here too: an empty generation is an error, not an empty view.
package enrich

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/model"
)

func TestPassthroughReturnsInputUnchanged(t *testing.T) {
	p := NewPassthrough()
	got, err := p.Enrich(context.Background(), TemplateData{Document: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestPassthroughSignatureIsStable(t *testing.T) {
	a := NewPassthrough()
	b := NewPassthrough()
	if a.Signature() != b.Signature() {
		t.Errorf("signatures differ: %s vs %s", a.Signature(), b.Signature())
	}
}

func TestLLMEnricherCallsGenerator(t *testing.T) {
	gen := model.NewFakeGenerator("test-model")
	prompt, err := ParsePrompt("test", []byte("Summarise: {{.Document}}"))
	if err != nil {
		t.Fatal(err)
	}

	e := NewLLMEnricher(gen, prompt, LLMConfig{SchemaVersion: 1})
	got, err := e.Enrich(context.Background(), TemplateData{
		Document: "some content",
		ViewName: "test-view",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Error("expected non-empty output from fake generator")
	}
	if got == "some content" {
		t.Error("expected transformed output, got passthrough")
	}
}

func TestLLMEnricherSignatureChangesWithPrompt(t *testing.T) {
	gen := model.NewFakeGenerator("test-model")
	p1, _ := ParsePrompt("a", []byte("prompt A: {{.Document}}"))
	p2, _ := ParsePrompt("b", []byte("prompt B: {{.Document}}"))

	e1 := NewLLMEnricher(gen, p1, LLMConfig{SchemaVersion: 1})
	e2 := NewLLMEnricher(gen, p2, LLMConfig{SchemaVersion: 1})

	if e1.Signature() == e2.Signature() {
		t.Error("different prompts should produce different signatures")
	}
}

func TestFragmentEnricherCallsGenerator(t *testing.T) {
	gen := model.NewFakeGenerator("test-model")
	prompt, err := ParsePrompt("frag", []byte("Summarise {{.Key}}: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}

	fe := NewFragmentLLMEnricher(gen, prompt, FragmentEnricherConfig{MaxInputChars: 8000})
	got, err := fe.Enrich(context.Background(), FragmentTemplateData{
		Content: "file contents",
		Key:     "src/main.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Error("expected non-empty output")
	}
}

// recordingGenerator captures the prompt it was given, which is how a test sees what the
// enricher decided to send.
type recordingGenerator struct {
	prompt string
}

func (g *recordingGenerator) Generate(_ context.Context, prompt string) (string, model.Usage, error) {
	g.prompt = prompt
	return "a summary", model.Usage{}, nil
}

func (g *recordingGenerator) Signature() string { return "recording" }

func TestFragmentEnricherTruncatesOversizedContent(t *testing.T) {
	gen := &recordingGenerator{}
	prompt, err := ParsePrompt("frag", []byte("Summarise {{.Key}}: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}

	fe := NewFragmentLLMEnricher(gen, prompt, FragmentEnricherConfig{MaxInputChars: 100})
	body := strings.Repeat("x", 5000)
	if _, err := fe.Enrich(context.Background(), FragmentTemplateData{Content: body, Key: "big.txt"}); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(gen.prompt, body) {
		t.Error("the whole file reached the generator; max_input_chars did not bound it")
	}
	if !strings.Contains(gen.prompt, truncationNotice) {
		t.Error("truncated content should carry the truncation notice")
	}
	// The bound is on content, so the rendered prompt is the bound plus the template around it.
	if len(gen.prompt) > 100+len(truncationNotice)+len("Summarise big.txt: ") {
		t.Errorf("prompt is %d characters, longer than the bounded content plus the template", len(gen.prompt))
	}
}

func TestTruncateChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		cut  bool
		want string // the retained head, before the notice
	}{
		{"under the bound", "hello", 10, false, "hello"},
		{"at the bound", "hello", 5, false, "hello"},
		{"no bound", "hello", 0, false, "hello"},
		// The cut prefers a line boundary once past the halfway point, so the tail is a whole
		// line rather than a split token.
		{"cuts at a line boundary", "aaaa\nbbbb\ncccc", 12, true, "aaaa\nbbbb"},
		// A boundary too early would throw away most of the budget, so the raw cut wins.
		{"ignores an early boundary", "a\n" + strings.Repeat("b", 20), 12, true, "a\nbbbbbbbbbb"},
		// Multi-byte runes are counted as runes and never split.
		{"counts runes", "日本語です", 3, true, "日本語"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := truncateChars(tt.in, tt.max)
			if cut != tt.cut {
				t.Fatalf("truncateChars() cut = %v, want %v", cut, tt.cut)
			}
			want := tt.want
			if tt.cut {
				want += truncationNotice
			}
			if got != want {
				t.Errorf("truncateChars() = %q, want %q", got, want)
			}
		})
	}
}

func TestLoadPromptFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tmpl")
	if err := os.WriteFile(path, []byte("Hello {{.ViewName}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := LoadPrompt(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.Render(TemplateData{ViewName: "world"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello world" {
		t.Errorf("got %q, want %q", got, "Hello world")
	}
}

func TestLoadPromptMissingFile(t *testing.T) {
	_, err := LoadPrompt("/nonexistent/path.tmpl")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParsePromptInvalidTemplate(t *testing.T) {
	_, err := ParsePrompt("bad", []byte("{{.Unclosed"))
	if err == nil {
		t.Error("expected error for invalid template")
	}
}

func TestPromptRendererBytes(t *testing.T) {
	raw := []byte("template content")
	p, err := ParsePrompt("test", raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Bytes()) != string(raw) {
		t.Error("Bytes() should return raw template content")
	}
}

// stubGenerator returns a fixed string, so a test can describe output an enricher must
// refuse rather than hoping a real model produces it.
type stubGenerator struct{ text string }

func (s stubGenerator) Generate(_ context.Context, _ string) (string, model.Usage, error) {
	return s.text, model.Usage{}, nil
}

func (s stubGenerator) Signature() string { return "stub" }

func TestValidateOutput(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		maxTokens int
		want      string
		wantErr   bool
	}{
		{"trims surrounding whitespace", "\n  summary  \n", 0, "summary", false},
		{"rejects empty", "", 0, "", true},
		{"rejects whitespace only", "   \n\t", 0, "", true},
		{"accepts within the length bound", "abcdefgh", 1, "abcdefgh", false},
		{"rejects over the length bound", "abcdefghi", 1, "", true},
		{"no bound when maxTokens is zero", "abcdefghijklmnop", 0, "abcdefghijklmnop", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateOutput("view test", tt.text, tt.maxTokens)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateOutput(%q) = %q, want an error", tt.text, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateOutput(%q): %v", tt.text, err)
			}
			if got != tt.want {
				t.Errorf("validateOutput(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestLLMEnricherRejectsEmptyGeneration(t *testing.T) {
	prompt, err := ParsePrompt("test", []byte("Summarise: {{.Document}}"))
	if err != nil {
		t.Fatal(err)
	}
	e := NewLLMEnricher(stubGenerator{text: "  \n "}, prompt, LLMConfig{SchemaVersion: 1})

	if _, err := e.Enrich(context.Background(), TemplateData{ViewName: "role"}); err == nil {
		t.Error("expected an error when the model returns no text")
	}
}

func TestFragmentEnricherTrimsGeneration(t *testing.T) {
	prompt, err := ParsePrompt("frag", []byte("Summarise {{.Key}}: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}
	fe := NewFragmentLLMEnricher(stubGenerator{text: "\n  a summary\n"}, prompt, FragmentEnricherConfig{})

	got, err := fe.Enrich(context.Background(), FragmentTemplateData{Key: "a.go", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a summary" {
		t.Errorf("got %q, want %q", got, "a summary")
	}
}
