// Tests for mapping an embeddings response back onto the request it answered.
//
// Every failure here is silent corruption if unchecked: a vector stored against the wrong
// view, or at the wrong width, looks fine everywhere until search answers are subtly wrong.
package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Vectors are placed by the index the provider reports, not by arrival order. Getting this
// wrong stores every vector against the wrong view with no error anywhere.
func TestEmbedderOutOfOrderResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, embeddingResponse{Data: []embeddingData{
			{Embedding: []float32{0, 0, 2}, Index: 2},
			{Embedding: []float32{0, 3, 0}, Index: 1},
			{Embedding: []float32{4, 0, 0}, Index: 0},
		}})
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL,
		Model:      "m",
		Dimensions: 3,
		Timeout:    5 * time.Second,
	})

	vecs, err := e.Embed(context.Background(), []string{"first", "second", "third"})
	if err != nil {
		t.Fatal(err)
	}

	// Each input's vector points along its own axis; reordering would swap them.
	for i, vec := range vecs {
		if vec[i] != 1 {
			t.Errorf("vector %d = %v, want the unit vector on axis %d", i, vec, i)
		}
	}
}

// Every way a response can fail to line up with the request it answered. All of these are
// silent corruption if unchecked: the wrong vector, or the wrong width, reaches the
// destination and nothing looks broken until search answers are subtly wrong.
func TestEmbedderResponseMappingErrors(t *testing.T) {
	tests := []struct {
		name       string
		inputs     []string
		dimensions int
		data       []embeddingData
		wantErr    []string
	}{
		{
			name:       "duplicate index",
			inputs:     []string{"a", "b"},
			dimensions: 2,
			data: []embeddingData{
				{Embedding: []float32{1, 0}, Index: 0},
				{Embedding: []float32{0, 1}, Index: 0},
			},
			wantErr: []string{"index 0"},
		},
		{
			name:       "index outside the batch",
			inputs:     []string{"a"},
			dimensions: 2,
			data:       []embeddingData{{Embedding: []float32{1, 0}, Index: 7}},
			wantErr:    []string{"index 7"},
		},
		{
			name:       "fewer embeddings than inputs",
			inputs:     []string{"a", "b"},
			dimensions: 3,
			data:       []embeddingData{{Embedding: []float32{1, 0, 0}, Index: 0}},
			wantErr:    []string{"1 embeddings for 2 inputs"},
		},
		{
			// The error names the settings to change, rather than surfacing later as an
			// opaque pgvector column mismatch (D8).
			name:       "width other than the contract",
			inputs:     []string{"a"},
			dimensions: 1024,
			data:       []embeddingData{{Embedding: []float32{1, 0}, Index: 0}},
			wantErr:    []string{"1024", "dimensions"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, embeddingResponse{Data: tt.data})
			}))
			defer srv.Close()

			e := NewEmbedder(OpenAIEmbedderConfig{
				BaseURL:    srv.URL,
				Model:      "m",
				Dimensions: tt.dimensions,
				Timeout:    5 * time.Second,
			})

			_, err := e.Embed(context.Background(), tt.inputs)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error should mention %q, got: %v", want, err)
				}
			}
		})
	}
}
