// View generation is optional and complete when declared, with independent input bounds.
package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestViewGeneratorFallbackAndComposeBound(t *testing.T) {
	c := valid(t)
	if c.Models.HasViewGenerator() || !reflect.DeepEqual(c.Models.EffectiveViewGenerator(), c.Models.Generator) {
		t.Fatal("omitted view role did not preserve the shared generator")
	}
	c.Models.ViewGenerator = c.Models.Generator
	c.Models.ViewGenerator.MaxInputChars = 16000
	c.Models.ViewGenerator.MaxOutputTokens = 2048
	c.Datatypes[0].Compose.MaxChars = 10000
	if err := c.Validate(); err != nil {
		t.Fatalf("view input limit should allow expanded composition: %v", err)
	}
	c.Models.ViewGenerator.MaxInputChars = 4096
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "models.view_generator.max_input_chars") {
		t.Fatalf("view role's input limit was ignored: %v", err)
	}
}

func TestViewGeneratorRejectsIncompleteDeclarations(t *testing.T) {
	for _, body := range []string{"{}", "{temperature: 0}", "{max_output_tokens: 0}", "{max_output_tokens: 2048}", "{request_options: {}}"} {
		t.Run(body, func(t *testing.T) {
			path := fixture(t, "minimal.yaml")
			writeLocal(t, path, "models:\n  view_generator: "+body+"\n")
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "models.view_generator") {
				t.Fatalf("incomplete role silently inherited the fragment generator: %v", err)
			}
		})
	}
}

func TestViewGeneratorEnvironmentAndSecretLayering(t *testing.T) {
	path := fixture(t, "minimal.yaml")
	writeLocal(t, path, `models:
  view_generator:
    driver: openai
    base_url: http://localhost:4000/v1
    model: view-model
    api_key_env: INGET_TEST_VIEW_KEY
    temperature: 0
    seed: 1
    max_output_tokens: 2048
    max_input_chars: 8192
    concurrency: 2
    timeout: 30s
`)
	t.Setenv("INGET_MODELS__VIEW_GENERATOR__MAX_OUTPUT_TOKENS", "4096")
	t.Setenv("INGET_TEST_VIEW_KEY", "test-view-secret")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models.EffectiveViewGenerator().MaxOutputTokens != 4096 || c.Models.Generator.MaxOutputTokens != 128 {
		t.Fatal("view override changed the fragment role or was ignored")
	}
	if key, err := c.Secret(c.Models.ViewGenerator.APIKeyEnv); err != nil || key != "test-view-secret" {
		t.Fatal("view credential was not resolved")
	}
	for _, key := range []string{"driver", "base_url", "model", "api_key_env", "temperature", "seed", "max_output_tokens", "max_input_chars", "concurrency", "timeout", "price_per_mtok_in", "price_per_mtok_out"} {
		if !slices.Contains(EnvKeys(), "models.view_generator."+key) {
			t.Errorf("view env key %s was not discovered", key)
		}
	}
}

func TestViewGeneratorRejectsIncompleteEnvironmentDeclaration(t *testing.T) {
	t.Setenv("INGET_MODELS__VIEW_GENERATOR__MAX_OUTPUT_TOKENS", "0")
	if _, err := Load(fixture(t, "minimal.yaml")); err == nil || !strings.Contains(err.Error(), "models.view_generator") {
		t.Fatalf("explicit zero environment setting silently inherited the fragment role: %v", err)
	}
}
