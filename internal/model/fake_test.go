// Tests for the deterministic fakes CI depends on (D14).
//
// The whole pipeline is testable without credentials only while these stay deterministic,
// so identical input must give an identical string and an identical unit vector across
// runs, and the fake embedder must honour the configured width.
package model

import (
	"context"
	"math"
	"testing"
)

func TestFakeGeneratorDeterminism(t *testing.T) {
	g := NewFakeGenerator("test-model")

	text1, usage1, err := g.Generate(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	text2, usage2, err := g.Generate(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}

	if text1 != text2 {
		t.Errorf("not deterministic: %q vs %q", text1, text2)
	}
	if usage1.TotalTokens != usage2.TotalTokens {
		t.Errorf("usage not deterministic: %d vs %d", usage1.TotalTokens, usage2.TotalTokens)
	}

	// Different input produces different output.
	text3, _, err := g.Generate(context.Background(), "different input")
	if err != nil {
		t.Fatal(err)
	}
	if text1 == text3 {
		t.Error("different inputs produced same output")
	}
}

func TestFakeGeneratorSignature(t *testing.T) {
	g1 := NewFakeGenerator("model-a")
	g2 := NewFakeGenerator("model-a")
	g3 := NewFakeGenerator("model-b")

	if g1.Signature() != g2.Signature() {
		t.Errorf("same model different signatures: %s vs %s", g1.Signature(), g2.Signature())
	}
	if g1.Signature() == g3.Signature() {
		t.Error("different models same signature")
	}
}

func TestFakeEmbedderDeterminism(t *testing.T) {
	e := NewFakeEmbedder("test-embed", 128)

	vecs1, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	vecs2, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}

	if len(vecs1) != 2 || len(vecs2) != 2 {
		t.Fatalf("unexpected lengths: %d, %d", len(vecs1), len(vecs2))
	}

	for i := range vecs1 {
		for j := range vecs1[i] {
			if vecs1[i][j] != vecs2[i][j] {
				t.Fatalf("not deterministic at [%d][%d]: %f vs %f", i, j, vecs1[i][j], vecs2[i][j])
			}
		}
	}

	// Different input produces different output.
	vecs3, err := e.Embed(context.Background(), []string{"different"})
	if err != nil {
		t.Fatal(err)
	}
	same := true
	for j := range min(len(vecs1[0]), len(vecs3[0])) {
		if vecs1[0][j] != vecs3[0][j] {
			same = false
			break
		}
	}
	if same {
		t.Error("different inputs produced same vectors")
	}
}

func TestFakeEmbedderUnitVectors(t *testing.T) {
	e := NewFakeEmbedder("test-embed", 1024)
	texts := []string{"alpha", "beta", "gamma", "delta", "epsilon"}

	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}

	for i, v := range vecs {
		if len(v) != 1024 {
			t.Errorf("vec[%d] length = %d, want 1024", i, len(v))
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		norm := math.Sqrt(sum)
		if math.Abs(norm-1.0) > 0.0001 {
			t.Errorf("vec[%d] norm = %f, want 1.0", i, norm)
		}
	}
}

func TestFakeEmbedderDimsAndModel(t *testing.T) {
	e := NewFakeEmbedder("my-model", 512)
	if e.Model() != "my-model" {
		t.Errorf("Model() = %q, want %q", e.Model(), "my-model")
	}
	if e.Dims() != 512 {
		t.Errorf("Dims() = %d, want 512", e.Dims())
	}
}

func TestFakeEmbedderSignature(t *testing.T) {
	e1 := NewFakeEmbedder("m", 128)
	e2 := NewFakeEmbedder("m", 128)
	e3 := NewFakeEmbedder("m", 256)

	if e1.Signature() != e2.Signature() {
		t.Errorf("same config different signatures: %s vs %s", e1.Signature(), e2.Signature())
	}
	if e1.Signature() == e3.Signature() {
		t.Error("different dims same signature")
	}
}
