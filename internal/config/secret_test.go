// Secret indirection tests. The behaviours worth pinning: values are snapshotted at load,
// an unset variable fails only when something asks for it, and no secret value can be
// reached by formatting the configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSecretsResolveAtLoad(t *testing.T) {
	t.Setenv("INGET_TEST_TOKEN", "ghp_example")
	cfg := valid(t)

	got, err := cfg.Secret(cfg.Sources[0].Auth.TokenEnv)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if got != "ghp_example" {
		t.Errorf("Secret() = %q, want the environment value", got)
	}
	if !cfg.HasSecret(cfg.Sources[0].Auth.TokenEnv) {
		t.Error("HasSecret() = false for a set variable")
	}
}

// TestSecretSnapshotIgnoresLaterEnvironmentChanges keeps an in-flight run using one set of
// credentials even if the process environment is mutated underneath it.
func TestSecretSnapshotIgnoresLaterEnvironmentChanges(t *testing.T) {
	t.Setenv("INGET_TEST_TOKEN", "first")
	cfg := valid(t)

	if err := os.Setenv("INGET_TEST_TOKEN", "second"); err != nil {
		t.Fatalf("setting env: %v", err)
	}
	got, err := cfg.Secret(cfg.Sources[0].Auth.TokenEnv)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if got != "first" {
		t.Errorf("Secret() = %q, want the value snapshotted at load", got)
	}
}

func TestMissingSecretFailsOnlyWhenAsked(t *testing.T) {
	cfg := valid(t) // INGET_TEST_TOKEN deliberately unset

	if _, err := cfg.Secret(cfg.Sources[0].Auth.TokenEnv); err == nil {
		t.Error("Secret() for an unset variable = nil error, want failure")
	} else if !strings.Contains(err.Error(), "INGET_TEST_TOKEN") {
		t.Errorf("error %q should name the variable", err)
	}
	if cfg.HasSecret(cfg.Sources[0].Auth.TokenEnv) {
		t.Error("HasSecret() = true for an unset variable")
	}
	if _, err := cfg.Secret(""); err == nil {
		t.Error("Secret(\"\") = nil error, want failure")
	}
}

// TestFormattingConfigCannotLeakSecrets is the reason resolved values live in an
// unexported field: anything that renders a Config sees variable names only.
func TestFormattingConfigCannotLeakSecrets(t *testing.T) {
	t.Setenv("INGET_TEST_TOKEN", "ghp_supersecret")
	cfg := valid(t)

	rendered := fmt.Sprintf("%+v %v", cfg, cfg.Sources[0].Auth.TokenEnv)

	if strings.Contains(rendered, "ghp_supersecret") {
		t.Error("formatted config contains a secret value")
	}
	if !strings.Contains(rendered, "INGET_TEST_TOKEN") {
		t.Error("formatted config should still show the variable name")
	}
}

func TestSecretRefIsDiscoveredAnywhereInTheSchema(t *testing.T) {
	t.Setenv("INGET_TEST_DSN", "postgres://localhost/inget")
	t.Setenv("INGET_TEST_GENERATOR_KEY", "sk-example")
	cfg := valid(t)

	for _, ref := range []SecretRef{cfg.Destinations[0].DSNEnv, cfg.Models.Generator.APIKeyEnv} {
		if !cfg.HasSecret(ref) {
			t.Errorf("secret %s was not resolved at load", ref)
		}
	}
}
