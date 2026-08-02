// Prompt template loading and rendering.
//
// Templates are Go text/template files loaded from disk. They receive either a
// TemplateData (for view prompts) or FragmentTemplateData (for fragment enrichment).
// Template loading is performed once at startup; rendering is per-item and must be safe
// for concurrent use with different data.
package enrich

import (
	"bytes"
	"fmt"
	"os"
	"text/template"
)

// PromptRenderer renders a prompt template with provided data. It is safe for concurrent
// use because text/template.Execute is goroutine-safe on a fully parsed template.
type PromptRenderer struct {
	tmpl *template.Template
	raw  []byte
}

// LoadPrompt reads a template file from path and parses it.
func LoadPrompt(path string) (*PromptRenderer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading prompt %s: %w", path, err)
	}
	return ParsePrompt(path, data)
}

// ParsePrompt parses a template from raw bytes, using name for error messages.
func ParsePrompt(name string, data []byte) (*PromptRenderer, error) {
	tmpl, err := template.New(name).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing prompt %s: %w", name, err)
	}
	return &PromptRenderer{tmpl: tmpl, raw: data}, nil
}

// Render executes the template with data and returns the resulting prompt string.
func (p *PromptRenderer) Render(data any) (string, error) {
	var buf bytes.Buffer
	if err := p.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering prompt: %w", err)
	}
	return buf.String(), nil
}

// Bytes returns the raw template bytes, used for signature computation.
func (p *PromptRenderer) Bytes() []byte {
	return p.raw
}
