// Wire types for the OpenAI-compatible API.
//
// These structs map to the JSON request and response bodies for /v1/chat/completions and
// /v1/embeddings. They intentionally cover only the fields this package uses; unknown
// fields are ignored on decode.
package model

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Seed        int           `json:"seed,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
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
	Message chatMessage `json:"message"`
}

type chatUsageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CacheHitTokens   int `json:"prompt_tokens_details.cached_tokens"`
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}
