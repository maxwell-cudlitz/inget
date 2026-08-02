package delta

import (
	"math"
	"strings"
	"testing"
)

func TestDrift(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical strings", "hello", "hello", 0.0},
		{"both empty", "", "", 0.0},
		{"completely different same length", "abc", "xyz", 1.0},
		{"one empty", "hello", "", 1.0},
		{"other empty", "", "world", 1.0},
		{"one edit", "kitten", "sitten", 1.0 / 6.0},
		{"partial overlap", "abcdef", "azced", 3.0 / 6.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Drift(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Drift(%q, %q) = %f, want %f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDriftSymmetric(t *testing.T) {
	a, b := "hello", "world"
	if Drift(a, b) != Drift(b, a) {
		t.Errorf("Drift is not symmetric: Drift(%q,%q)=%f, Drift(%q,%q)=%f",
			a, b, Drift(a, b), b, a, Drift(b, a))
	}
}

// Distance is measured in runes, so the denominator must be too. Normalising by byte
// length understates drift on every multi-byte string and would suppress re-embedding.
func TestDriftNonASCII(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		// 5 runes, 6 bytes: one substitution is 1/5, not 1/6.
		{"accented substitution", "héllo", "hello", 1.0 / 5.0},
		// 3 runes, 9 bytes: one deletion is 1/3, not 1/9.
		{"cjk deletion", "日本語", "日本", 1.0 / 3.0},
		// 2 runes, 8 bytes: replacing both is total drift.
		{"emoji replacement", "🎉🎊", "🎈🎁", 1.0},
		{"mixed script", "café-日本", "cafe-日本", 1.0 / 7.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Drift(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Drift(%q, %q) = %f, want %f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// A one-character edit in CJK text must cross the shipped 0.02 default the same way it
// would in ASCII text of the same length.
func TestDriftThresholdIsScriptIndependent(t *testing.T) {
	ascii := strings.Repeat("a", 100)
	cjk := strings.Repeat("日", 100)

	asciiDrift := Drift(ascii, strings.Repeat("a", 99)+"b")
	cjkDrift := Drift(cjk, strings.Repeat("日", 99)+"語")

	if math.Abs(asciiDrift-cjkDrift) > 1e-9 {
		t.Errorf("same edit scored differently by script: ascii=%f cjk=%f", asciiDrift, cjkDrift)
	}
	if !DriftExceedsThreshold(cjk, strings.Repeat("日", 99)+"語", 0.005) {
		t.Error("a 1%% edit in CJK text did not exceed a 0.5%% threshold")
	}
}

func TestDriftExceedsThreshold(t *testing.T) {
	tests := []struct {
		name      string
		a, b      string
		threshold float64
		want      bool
	}{
		{"below threshold", "hello", "hallo", 0.5, false},
		{"at threshold", "abc", "xyz", 1.0, false},
		{"above threshold", "abc", "xyz", 0.5, true},
		{"identical at zero", "same", "same", 0.0, false},
		{"empty strings at zero", "", "", 0.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DriftExceedsThreshold(tt.a, tt.b, tt.threshold)
			if got != tt.want {
				t.Errorf("DriftExceedsThreshold(%q, %q, %f) = %v, want %v",
					tt.a, tt.b, tt.threshold, got, tt.want)
			}
		})
	}
}
