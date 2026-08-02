// Tests for secret detection.
//
// The assertions are about behaviour at the boundary — something matched, nothing matched, scanning
// is off — rather than about which of gitleaks' rules fired. Pinning a specific rule ID would make
// this suite fail on a ruleset update that improved detection.
package github

import (
	"strings"
	"sync"
	"testing"
)

// fakeToken assembles a value the default ruleset recognizes. It is built rather than written out
// so this file does not itself read as a leak.
func fakeToken() string { return "ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a" }

func TestScannerDetectsASecret(t *testing.T) {
	s, err := newScanner(true)
	if err != nil {
		t.Fatalf("newScanner() = %v", err)
	}
	rules := s.rules([]byte("const token = \"" + fakeToken() + "\"\n"))
	if len(rules) == 0 {
		t.Fatal("rules() found nothing in content holding a recognizable token")
	}
	for _, rule := range rules {
		if strings.Contains(rule, fakeToken()) {
			t.Error("a returned rule ID contains the secret value")
		}
	}
}

func TestScannerPassesCleanContent(t *testing.T) {
	s, err := newScanner(true)
	if err != nil {
		t.Fatalf("newScanner() = %v", err)
	}
	for _, content := range []string{
		"package store\n\ntype Store struct{}\n",
		"# inget\n\nA pipeline.\n",
		"",
	} {
		if rules := s.rules([]byte(content)); len(rules) != 0 {
			t.Errorf("rules(%q) = %v, want none", content, rules)
		}
	}
}

func TestDisabledScannerFindsNothing(t *testing.T) {
	s, err := newScanner(false)
	if err != nil {
		t.Fatalf("newScanner(false) = %v", err)
	}
	if s.enabled() {
		t.Error("a disabled scanner reports itself enabled")
	}
	if rules := s.rules([]byte("const token = \"" + fakeToken() + "\"\n")); rules != nil {
		t.Errorf("rules() = %v with scanning off, want nil", rules)
	}
}

func TestNilScannerFindsNothing(t *testing.T) {
	var s *scanner
	if rules := s.rules([]byte(fakeToken())); rules != nil {
		t.Errorf("rules() on a nil scanner = %v, want nil", rules)
	}
}

// Items are fetched in parallel and share one detector, so concurrent scanning has to be safe. The
// race detector is what makes this test worth having.
func TestScannerIsSafeForConcurrentUse(t *testing.T) {
	s, err := newScanner(true)
	if err != nil {
		t.Fatalf("newScanner() = %v", err)
	}
	contents := [][]byte{
		[]byte("clean file\n"),
		[]byte("token = \"" + fakeToken() + "\"\n"),
		[]byte("package main\n"),
	}
	var wg sync.WaitGroup
	for range 8 {
		for _, content := range contents {
			wg.Add(1)
			go func(c []byte) {
				defer wg.Done()
				s.rules(c)
			}(content)
		}
	}
	wg.Wait()
}
