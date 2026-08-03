// Tests for provider request options.
//
// The cases pin what the merge may and may not do: an unknown provider parameter reaches the
// request body, a parameter this client owns cannot be replaced through the back door, and the
// signature moves when the options move — otherwise a thinking toggle would serve cached output
// generated under the opposite setting.
package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeRequestMergesOptions(t *testing.T) {
	body := chatRequest{Model: "m", Messages: []chatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 64}
	options := map[string]any{
		"thinking":         map[string]any{"type": "disabled"},
		"reasoning_effort": "high",
	}

	data, err := encodeRequest(body, options)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", decoded["reasoning_effort"])
	}
	thinking, ok := decoded["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want {type: disabled}", decoded["thinking"])
	}
	// The typed fields must survive the merge.
	if decoded["model"] != "m" || decoded["max_tokens"] != float64(64) {
		t.Errorf("typed fields lost: model=%v max_tokens=%v", decoded["model"], decoded["max_tokens"])
	}
}

func TestEncodeRequestWithoutOptionsIsUnchanged(t *testing.T) {
	body := chatRequest{Model: "m", Messages: []chatMessage{{Role: "user", Content: "hi"}}}
	withNil, err := encodeRequest(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(withNil) != string(direct) {
		t.Errorf("encodeRequest(nil) = %s, want %s", withNil, direct)
	}
}

func TestValidateRequestOptionsRejectsReservedKeys(t *testing.T) {
	for _, key := range []string{"max_tokens", "messages", "model", "seed", "stream", "temperature"} {
		t.Run(key, func(t *testing.T) {
			err := ValidateRequestOptions(map[string]any{key: "x"})
			if err == nil {
				t.Fatalf("expected %s to be refused", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error %q does not name %s", err, key)
			}
		})
	}
	if err := ValidateRequestOptions(map[string]any{"": "x"}); err == nil {
		t.Error("expected an empty option name to be refused")
	}
	if err := ValidateRequestOptions(map[string]any{"thinking": "disabled"}); err != nil {
		t.Errorf("a provider parameter was refused: %v", err)
	}
}

// A reserved key cannot be smuggled past configuration by constructing the client directly.
func TestEncodeRequestRefusesReservedKeys(t *testing.T) {
	body := chatRequest{Model: "m"}
	if _, err := encodeRequest(body, map[string]any{"max_tokens": 1}); err == nil {
		t.Error("expected encodeRequest to refuse a reserved key")
	}
}

func TestSignatureCoversRequestOptions(t *testing.T) {
	base := OpenAIGeneratorConfig{Model: "m", MaxOutputTokens: 1024, MaxInputChars: 1000}
	thinking := base
	thinking.RequestOptions = map[string]any{"thinking": map[string]any{"type": "disabled"}}

	plain := NewGenerator(base).Signature()
	toggled := NewGenerator(thinking).Signature()
	if plain == toggled {
		t.Error("request options must change the generator signature")
	}

	// Two maps holding the same options must agree, whatever order they were written in: the
	// signature is a claim about the request, and the request is identical.
	same := base
	same.RequestOptions = map[string]any{"thinking": map[string]any{"type": "disabled"}}
	if NewGenerator(same).Signature() != toggled {
		t.Error("equal request options produced different signatures")
	}
}
