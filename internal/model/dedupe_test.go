// Tests for sending a repeated text once.
//
// The behaviour is a response to a measured server bug: TEI 1.9.3 on Metal returned a wrong,
// internally consistent vector for a text repeated inside one request, nondeterministically,
// while the same text sent alone embedded correctly. Sending it once is also what "identical
// text has an identical embedding" requires, so the client no longer depends on the server
// getting duplicates right.
package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// decodeEmbeddingRequest reads the request body as an embeddings request.
func decodeEmbeddingRequest(t *testing.T, r *http.Request) embeddingRequest {
	t.Helper()
	var req embeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decoding embeddings request: %v", err)
	}
	return req
}

func TestEmbedderSendsARepeatedTextOnce(t *testing.T) {
	var sent [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeEmbeddingRequest(t, r)
		sent = append(sent, req.Input)

		data := make([]embeddingData, len(req.Input))
		for i, text := range req.Input {
			// A distinct axis per input, so a mis-mapped vector is visible rather than
			// plausible: "a" embeds as x, "b" as y, "c" as z.
			vec := make([]float32, 3)
			vec[int(text[0]-'a')%3] = 1
			data[i] = embeddingData{Embedding: vec, Index: i}
		}
		writeJSON(t, w, embeddingResponse{Data: data})
	}))
	defer srv.Close()

	e := NewEmbedder(OpenAIEmbedderConfig{
		BaseURL:    srv.URL,
		Model:      "test-embed",
		Dimensions: 3,
		Timeout:    5 * time.Second,
	})

	vecs, err := e.Embed(t.Context(), []string{"a", "b", "a", "c", "b", "a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("made %d requests, want 1", len(sent))
	}
	if got := sent[0]; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("sent %v, want the three distinct texts in first-appearance order", got)
	}
	if len(vecs) != 6 {
		t.Fatalf("got %d vectors for 6 inputs", len(vecs))
	}

	want := map[int]int{0: 0, 1: 1, 2: 0, 3: 2, 4: 1, 5: 0} // input index -> hot axis
	for input, axis := range want {
		if vecs[input][axis] != 1 {
			t.Errorf("vector %d = %v, want axis %d set", input, vecs[input], axis)
		}
	}

	// Each occurrence owns its slice: mutating one must not disturb the others.
	vecs[0][0] = 42
	if vecs[2][0] != 1 || vecs[5][0] != 1 {
		t.Error("repeated inputs share one backing array")
	}
}

func TestDedupe(t *testing.T) {
	cases := []struct {
		name   string
		texts  []string
		unique []string
		at     []int
	}{
		{"no duplicates", []string{"a", "b"}, []string{"a", "b"}, []int{0, 1}},
		{"all identical", []string{"a", "a", "a"}, []string{"a"}, []int{0, 0, 0}},
		{"interleaved", []string{"b", "a", "b"}, []string{"b", "a"}, []int{0, 1, 0}},
		{"empty strings count as a text", []string{"", "a", ""}, []string{"", "a"}, []int{0, 1, 0}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unique, at := dedupe(c.texts)
			if len(unique) != len(c.unique) {
				t.Fatalf("unique = %v, want %v", unique, c.unique)
			}
			for i := range unique {
				if unique[i] != c.unique[i] {
					t.Errorf("unique[%d] = %q, want %q", i, unique[i], c.unique[i])
				}
			}
			for i := range at {
				if at[i] != c.at[i] {
					t.Errorf("at = %v, want %v", at, c.at)
					break
				}
			}
		})
	}
}
