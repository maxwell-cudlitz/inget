package delta

import (
	"math"
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
