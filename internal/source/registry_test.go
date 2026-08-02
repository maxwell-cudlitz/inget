// Tests for the driver registry and the configuration bridge.
//
// The registry is what makes adding a connector one directory plus one Register call, so the cases
// here are the ones a new driver would trip over: an unknown name must say what is registered, and a
// source with no resolvable token must fail before a run takes a lock.
package source

import (
	"context"
	"strings"
	"testing"
)

// stub is a connector that does nothing, for registry tests that never call one.
type stub struct{ name string }

func (s stub) Name() string                                                   { return s.name }
func (s stub) Datatypes() []string                                            { return []string{"stub/item"} }
func (s stub) Close() error                                                   { return nil }
func (s stub) List(context.Context, string, ListQuery, func(Ref) error) error { return nil }
func (s stub) Fetch(context.Context, string, Ref) (Result, error)             { return Result{}, nil }

func TestRegisterAndOpen(t *testing.T) {
	const driver = "registry-test-open"
	Register(driver, func(_ context.Context, opts Options) (Connector, error) {
		return stub{name: opts.Name}, nil
	})

	conn, err := Open(t.Context(), Options{Name: "stubby", Driver: driver})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if conn.Name() != "stubby" {
		t.Errorf("Name() = %q, want the configured source name", conn.Name())
	}
}

func TestOpenReportsWhatIsRegistered(t *testing.T) {
	const driver = "registry-test-listed"
	Register(driver, func(context.Context, Options) (Connector, error) { return stub{}, nil })

	_, err := Open(t.Context(), Options{Name: "typo", Driver: "no-such-driver"})
	if err == nil {
		t.Fatal("Open() with an unknown driver = nil error, want failure")
	}
	if !strings.Contains(err.Error(), driver) {
		t.Errorf("error %q should list the registered drivers so the typo is obvious", err)
	}
}

func TestOpenRequiresADriver(t *testing.T) {
	if _, err := Open(t.Context(), Options{Name: "nameless"}); err == nil {
		t.Fatal("Open() with no driver = nil error, want failure")
	}
}

// Two implementations answering to one configuration value is a build-time mistake, so it panics
// rather than letting a run pick one at random.
func TestRegisterRejectsDuplicates(t *testing.T) {
	const driver = "registry-test-duplicate"
	Register(driver, func(context.Context, Options) (Connector, error) { return stub{}, nil })

	defer func() {
		if recover() == nil {
			t.Error("registering a driver twice did not panic")
		}
	}()
	Register(driver, func(context.Context, Options) (Connector, error) { return stub{}, nil })
}

func TestRegisteredIsSorted(t *testing.T) {
	names := Registered()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Registered() = %v, want sorted output so an error message is stable", names)
		}
	}
}
