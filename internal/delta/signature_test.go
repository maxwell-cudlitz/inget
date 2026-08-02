package delta

import (
	"reflect"
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

// TestBuildSignatureCoversEveryField walks SignatureInput by reflection and asserts that
// mutating each field changes the digest.
//
// D2 requires every input that can change enricher output to be a field of this struct,
// but a field is only a real guard once it reaches the hash. BuildSignature lists its
// fields by hand, so a field added to the struct and forgotten there would silently stop
// invalidating anything. This test is what turns that convention into an enforced
// property: it fails on the new field without anyone remembering to extend a list.
func TestBuildSignatureCoversEveryField(t *testing.T) {
	base := SignatureInput{
		ModelID:         "gpt-4o",
		PromptBytes:     []byte("summarize this"),
		MaxInputChars:   8000,
		MaxOutputTokens: 1024,
		SchemaVersion:   2,
		EnricherOptions: map[string]string{"lang": "en"},
	}
	baseSig := BuildSignature(base)

	typ := reflect.TypeOf(base)
	for i := range typ.NumField() {
		field := typ.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			mutated := base
			mutateField(t, field.Name, reflect.ValueOf(&mutated).Elem().Field(i))

			if BuildSignature(mutated) == baseSig {
				t.Errorf("mutating %s left the signature unchanged: BuildSignature must encode "+
					"every field of SignatureInput (D2)", field.Name)
			}
		})
	}
}

// mutateField sets v to a value different from the one it holds. Every field is replaced
// rather than modified in place so the caller's copy stays untouched. An unhandled kind
// fails loudly: silently skipping it would make the completeness check pass for a field
// it never tested.
func mutateField(t *testing.T, name string, v reflect.Value) {
	t.Helper()

	original := reflect.New(v.Type()).Elem()
	original.Set(v)

	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-mutated")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes(append(append([]byte{}, v.Bytes()...), "-mutated"...))
		}
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String && v.Type().Elem().Kind() == reflect.String {
			m := map[string]string{"mutation-sentinel": "1"}
			for _, k := range v.MapKeys() {
				m[k.String()] = v.MapIndex(k).String()
			}
			v.Set(reflect.ValueOf(m))
		}
	}

	if reflect.DeepEqual(original.Interface(), v.Interface()) {
		t.Fatalf("field %s has type %s, which mutateField cannot change: extend it so "+
			"signature completeness stays enforced", name, v.Type())
	}
}
