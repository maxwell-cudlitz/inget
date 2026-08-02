package enrich

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/model"
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
