// Estimation measures rendered Unicode text without invoking the generator.
package enrich

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/maxwell-cudlitz/inget/internal/model"
)

func TestLLMPromptCharsIncludesWrapperAndMetadataWithoutGeneration(t *testing.T) {
	prompt, err := ParsePrompt("view", []byte(`View {{.ViewName}} by {{index .Metadata "owner"}}: {{.Document}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := NewLLMEnricher(noGenerate{}, prompt, LLMConfig{})
	for _, doc := range []string{"", "日本語 emoji 😀"} {
		got, err := e.PromptChars(TemplateData{
			ViewName: "purpose", Metadata: map[string]string{"owner": "équipe"}, Document: doc,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := utf8.RuneCountInString("View purpose by équipe: " + doc); got != want {
			t.Errorf("PromptChars(%q) = %d, want %d", doc, got, want)
		}
	}
}

func TestFragmentPromptCharsUsesRuntimeTruncationWithoutGeneration(t *testing.T) {
	prompt, err := ParsePrompt("fragment", []byte("File {{.Key}}: {{.Content}}"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, content, want string
		max                 int
	}{
		{"wrapper", "", "", 3},
		{"unicode under bound", "日本語", "日本語", 3},
		{"unicode truncated", "日本語です", "日本語" + truncationNotice, 3},
		{"line boundary", "aaaa\nbbbb\ncccc", "aaaa\nbbbb" + truncationNotice, 12},
		{"unbounded", strings.Repeat("x", 50), strings.Repeat("x", 50), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewFragmentLLMEnricher(noGenerate{}, prompt, FragmentEnricherConfig{MaxInputChars: tt.max})
			got, err := e.PromptChars(FragmentTemplateData{Key: "é.go", Content: tt.content})
			if err != nil {
				t.Fatal(err)
			}
			if want := utf8.RuneCountInString("File é.go: " + tt.want); got != want {
				t.Errorf("PromptChars = %d, want %d", got, want)
			}
			if e.TruncationNoticeChars() != utf8.RuneCountInString(truncationNotice) {
				t.Error("truncation notice count differs from runtime notice")
			}
		})
	}
}

func TestPromptCharsPropagatesRenderFailures(t *testing.T) {
	prompt, err := ParsePrompt("bad-field", []byte("{{.UnknownField}}"))
	if err != nil {
		t.Fatal(err)
	}
	view := NewLLMEnricher(noGenerate{}, prompt, LLMConfig{})
	fragment := NewFragmentLLMEnricher(noGenerate{}, prompt, FragmentEnricherConfig{})
	if got, err := view.PromptChars(TemplateData{ViewName: "purpose"}); err == nil || got != 0 {
		t.Fatalf("view render failure = %d, %v", got, err)
	}
	if got, err := fragment.PromptChars(FragmentTemplateData{Key: "README.md"}); err == nil || got != 0 {
		t.Fatalf("fragment render failure = %d, %v", got, err)
	}
}

// noGenerate turns any accidental estimation-time inference into an immediate test failure.
type noGenerate struct{}

func (noGenerate) Signature() string { return "never-generate" }
func (noGenerate) Generate(context.Context, string) (string, model.Usage, error) {
	panic("prompt measurement must not generate")
}
