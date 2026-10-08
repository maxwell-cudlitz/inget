// Prompt measurement uses the actual renderer and input truncation, without requesting generation.
// Planning can count fixed prompt wrappers with empty content while retaining normal render errors.
package enrich

import (
	"fmt"
	"unicode/utf8"
)

// PromptChars returns the rendered view prompt's Unicode character count without model calls.
func (e *LLMEnricher) PromptChars(data TemplateData) (int, error) {
	prompt, err := e.prompt.Render(data)
	if err != nil {
		return 0, fmt.Errorf("measuring prompt for view %s: %w", data.ViewName, err)
	}
	return utf8.RuneCountInString(prompt), nil
}

// PromptChars measures the rendered fragment prompt after the same truncation Enrich applies.
func (e *FragmentLLMEnricher) PromptChars(data FragmentTemplateData) (int, error) {
	if truncated, cut := truncateChars(data.Content, e.cfg.MaxInputChars); cut {
		data.Content = truncated
	}
	prompt, err := e.prompt.Render(data)
	if err != nil {
		return 0, fmt.Errorf("measuring prompt for fragment %s: %w", data.Key, err)
	}
	return utf8.RuneCountInString(prompt), nil
}

// TruncationNoticeChars returns the character count appended when fragment content is cut.
func (e *FragmentLLMEnricher) TruncationNoticeChars() int {
	return utf8.RuneCountInString(truncationNotice)
}
