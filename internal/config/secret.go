// Secret indirection. Configuration never contains a credential; it contains the *name*
// of an environment variable that holds one (`token_env`, `api_key_env`, `dsn_env`).
//
// Load snapshots the value of every declared variable into an unexported map, so a
// marshalled Config cannot leak a secret, and a mid-run change to the process
// environment cannot change what an in-flight run is using. Absence is not a load
// failure: a run that only touches GitHub should not require the Monday token. The
// component that needs a value reports the failure, via Secret, at the point where it
// can say what it was trying to do.
package config

import (
	"fmt"
	"os"
	"reflect"
)

// SecretRef is the name of an environment variable holding a secret. Its String method
// returns the name, never the value, so a SecretRef is safe to log.
type SecretRef string

// Name returns the environment variable name.
func (s SecretRef) Name() string { return string(s) }

// String returns the environment variable name, so formatting a SecretRef cannot leak
// the secret it points at.
func (s SecretRef) String() string { return string(s) }

// Secret returns the value snapshotted at load for ref.
func (c *Config) Secret(ref SecretRef) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("no secret environment variable is configured")
	}
	value, ok := c.secrets[ref.Name()]
	if !ok || value == "" {
		return "", fmt.Errorf("environment variable %s is empty or unset", ref.Name())
	}
	return value, nil
}

// HasSecret reports whether ref resolved to a non-empty value at load. Callers use it to
// choose between an authenticated and an anonymous path without triggering an error.
func (c *Config) HasSecret(ref SecretRef) bool {
	_, err := c.Secret(ref)
	return err == nil
}

// String summarises the configuration without any secret value.
//
// It exists because fmt reads unexported fields when printing with %+v: keeping resolved
// secrets in an unexported map stops encoding/json and gopkg.in/yaml from reaching them,
// but not fmt. Implementing Stringer takes that last path away, at the cost of %+v no
// longer dumping the whole struct.
func (c Config) String() string {
	return fmt.Sprintf("config{version:%d sources:%d destinations:%d datatypes:%d secrets:%d}",
		c.Version, len(c.Sources), len(c.Destinations), len(c.Datatypes), len(c.secrets))
}

// resolveSecrets snapshots every SecretRef in the config. The refs are discovered by
// walking the decoded value rather than by an enumerated list, so a new *_env field is
// resolved the moment it is declared in the schema.
func (c *Config) resolveSecrets() {
	c.secrets = make(map[string]string)
	for _, l := range flatten("", reflect.ValueOf(c).Elem()) {
		ref, ok := l.value.Interface().(SecretRef)
		if !ok || ref == "" {
			continue
		}
		if value, found := os.LookupEnv(ref.Name()); found {
			c.secrets[ref.Name()] = value
		}
	}
}
