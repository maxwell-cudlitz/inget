package delta

import (
	"strings"
	"testing"
)

func TestBuildSignatureStability(t *testing.T) {
	input := SignatureInput{
		ModelID:         "gpt-4o",
		PromptBytes:     []byte("summarize this"),
		MaxInputChars:   8000,
		MaxOutputTokens: 1024,
		SchemaVersion:   2,
		EnricherOptions: map[string]string{"lang": "en", "format": "markdown"},
	}
	sig1 := BuildSignature(input)
	sig2 := BuildSignature(input)

	if sig1 != sig2 {
		t.Fatalf("identical inputs produced different signatures:\n  %s\n  %s", sig1, sig2)
	}
	if !strings.HasPrefix(sig1, "sha256:") {
		t.Errorf("signature = %q, want sha256: prefix", sig1)
	}
}

func TestBuildSignatureChangesOnAnyField(t *testing.T) {
	base := SignatureInput{
		ModelID:         "gpt-4o",
		PromptBytes:     []byte("summarize this"),
		MaxInputChars:   8000,
		MaxOutputTokens: 1024,
		SchemaVersion:   2,
		EnricherOptions: map[string]string{"lang": "en"},
	}
	baseSig := BuildSignature(base)

	mutations := []struct {
		name   string
		mutate func(SignatureInput) SignatureInput
	}{
		{"ModelID", func(s SignatureInput) SignatureInput { s.ModelID = "claude-3"; return s }},
		{"PromptBytes", func(s SignatureInput) SignatureInput { s.PromptBytes = []byte("different"); return s }},
		{"MaxInputChars", func(s SignatureInput) SignatureInput { s.MaxInputChars = 4000; return s }},
		{"MaxOutputTokens", func(s SignatureInput) SignatureInput { s.MaxOutputTokens = 512; return s }},
		{"SchemaVersion", func(s SignatureInput) SignatureInput { s.SchemaVersion = 3; return s }},
		{"EnricherOptions add", func(s SignatureInput) SignatureInput {
			s.EnricherOptions = map[string]string{"lang": "en", "extra": "yes"}
			return s
		}},
		{"EnricherOptions value", func(s SignatureInput) SignatureInput {
			s.EnricherOptions = map[string]string{"lang": "fr"}
			return s
		}},
		{"EnricherOptions nil", func(s SignatureInput) SignatureInput {
			s.EnricherOptions = nil
			return s
		}},
	}

	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			mutated := tt.mutate(base)
			sig := BuildSignature(mutated)
			if sig == baseSig {
				t.Errorf("changing %s did not change the signature", tt.name)
			}
		})
	}
}

func TestBuildSignatureEmptyOptions(t *testing.T) {
	withNil := SignatureInput{ModelID: "m", SchemaVersion: 1}
	withEmpty := SignatureInput{ModelID: "m", SchemaVersion: 1, EnricherOptions: map[string]string{}}

	if BuildSignature(withNil) != BuildSignature(withEmpty) {
		t.Error("nil and empty EnricherOptions should produce the same signature")
	}
}
