// Wire types for the OpenAI-compatible API.
//
// These structs map to the JSON request and response bodies for /v1/chat/completions and
// /v1/embeddings. They cover only the fields this package uses; unknown fields are ignored
// on decode.
package model

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// Temperature and Seed carry no omitempty: zero is the value this pipeline wants for
	// temperature, and seed 0 is a legitimate seed. Omitting either would let the provider
	// pick its own default while the signature still claimed determinism.
	Temperature float64 `json:"temperature"`
	Seed        int     `json:"seed"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice   `json:"choices"`
	Usage   chatUsageBlock `json:"usage"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatUsageBlock struct {
	PromptTokens        int                 `json:"prompt_tokens"`
	CompletionTokens    int                 `json:"completion_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	PromptTokensDetails promptTokensDetails `json:"prompt_tokens_details"`
}

// promptTokensDetails is the OpenAI extension block reporting context-cache hits, which
// DeepSeek and Kimi both populate and `inget plan` uses for cost estimates.
//
// It has to be a nested struct. encoding/json matches tag names literally and has no
// notion of a path, so a `json:"prompt_tokens_details.cached_tokens"` tag on a flat field
// compiles, never matches anything, and reports zero cache hits forever.
type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// embeddingRequest carries no "dimensions" field on purpose: MRL truncation is applied
// client-side (see openAIEmbedder.fit) so that providers which do not implement the
// parameter — TEI among them — behave identically to those that do.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}
