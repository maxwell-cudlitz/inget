// Tests for the generator's completeness guard.
//
// A provider can answer 200 with a completion that is truncated or empty, and the pipeline
// would otherwise cache, embed and upsert that as a legitimate view. These cases pin the
// guard that rejects it. Measured against the Moonshot API on 2026-08-02: kimi-k3 returned
// HTTP 200 with finish_reason "length" and empty content when max_tokens was too small,
// having spent the whole output budget on reasoning tokens.
package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGeneratorRejectsTruncatedOrEmptyCompletions(t *testing.T) {
	tests := []struct {
		name     string
		response chatResponse
		wantErr  string
		wantOK   bool
		wantText string
	}{
		{
			name: "finish_reason length is an error",
			response: chatResponse{
				Choices: []chatChoice{{
					Message:      chatMessage{Role: "assistant", Content: "partial output"},
					FinishReason: "length",
				}},
				Usage: chatUsageBlock{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			},
			wantErr: "max_output_tokens",
		},
		{
			name: "empty content is an error",
			response: chatResponse{
				Choices: []chatChoice{{
					Message:      chatMessage{Role: "assistant", Content: ""},
					FinishReason: "stop",
				}},
				Usage: chatUsageBlock{PromptTokens: 10, CompletionTokens: 0, TotalTokens: 10},
			},
			wantErr: "empty content",
		},
		{
			name: "whitespace-only content is an error",
			response: chatResponse{
				Choices: []chatChoice{{
					Message:      chatMessage{Role: "assistant", Content: "   \n\t  "},
					FinishReason: "stop",
				}},
				Usage: chatUsageBlock{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11},
			},
			wantErr: "empty content",
		},
		{
			name: "normal completion succeeds",
			response: chatResponse{
				Choices: []chatChoice{{
					Message:      chatMessage{Role: "assistant", Content: "good output"},
					FinishReason: "stop",
				}},
				Usage: chatUsageBlock{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			},
			wantOK:   true,
			wantText: "good output",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, tt.response)
			}))
			defer srv.Close()

			g := NewGenerator(OpenAIGeneratorConfig{
				BaseURL:         srv.URL,
				Model:           "test-model",
				MaxOutputTokens: 64,
				Timeout:         5 * time.Second,
			})

			text, _, err := g.Generate(context.Background(), "prompt")
			if tt.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if text != tt.wantText {
					t.Errorf("text = %q, want %q", text, tt.wantText)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
