// Prompt-size accounting for estimates, without reading raw blobs or calling models.
//
// Artifact byte counts approximate raw characters. Cached derived text is counted by
// rune, while unknown derivations use the measured output mean. Composition headings,
// separators, caps and real rendered prompt wrappers are accounted for explicitly.
package pipeline

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/delta"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
)

type viewPromptSizer interface {
	PromptChars(enrich.TemplateData) (int, error)
}

func fragmentPromptChars(deps Deps, frag artifact.Fragment, maxChars int) (int, error) {
	wrapper, err := deps.FragEnricher.PromptChars(enrich.FragmentTemplateData{Key: frag.Key})
	if err != nil {
		return 0, fmt.Errorf("sizing fragment prompt for %s: %w", frag.Key, err)
	}
	chars := boundChars(int(frag.Bytes), maxChars) + wrapper
	if maxChars > 0 && frag.Bytes > int64(maxChars) {
		chars += deps.FragEnricher.TruncationNoticeChars()
	}
	return chars, nil
}

func viewPromptChars(deps Deps, rec *artifact.Record, view config.View, fallback float64) (float64, error) {
	sizer, ok := deps.Enrichers[view.Name].(viewPromptSizer)
	if !ok {
		return fallback, nil
	}
	chars, err := sizer.PromptChars(enrich.TemplateData{
		Metadata: AnnotateMetadata(rec.Metadata, deps.Config.MetadataFields, nil), ViewName: view.Name,
	})
	if err != nil {
		return 0, fmt.Errorf("sizing view prompt for %s: %w", view.Name, err)
	}
	return float64(chars), nil
}

func expectedFragment(ctx context.Context, deps Deps, p *EstimateProfile, frag artifact.Fragment,
	frags fragEstimate, key string, cached bool, expected *ExpectedEstimate) error {
	if cached {
		text, found, err := deps.State.PeekDerivation(ctx, key)
		if err != nil {
			return fmt.Errorf("sizing cached derivation for %s: %w", frag.Key, err)
		}
		if !found {
			return fmt.Errorf("derivation cache changed while estimating fragment %s", frag.Key)
		}
		frags.chars[key] = float64(utf8.RuneCountInString(text))
		return nil
	}
	frags.chars[key] = p.Fragment.OutputChars
	chars, err := fragmentPromptChars(deps, frag, frags.maxChars)
	if err != nil {
		return err
	}
	expected.add(*p.Fragment, float64(chars))
	return nil
}

func expectedView(deps Deps, p *EstimateProfile, rec *artifact.Record, view config.View,
	frags fragEstimate, expected *ExpectedEstimate) error {
	stage := p.Views[view.Name]
	chars := composedEstimateChars(rec, view.DependsOn, frags)
	if chars == 0 {
		return nil
	}
	if cap := deps.Config.ComposeMaxChars; cap > 0 {
		chars = min(chars, float64(cap))
	}
	wrapper, err := viewPromptChars(deps, rec, view, stage.PromptChars)
	if err != nil {
		return err
	}
	expected.add(stage, chars+wrapper)
	return nil
}

func composedEstimateChars(rec *artifact.Record, dependsOn []string, frags fragEstimate) float64 {
	total, entries := 0.0, 0
	for _, frag := range rec.Fragments {
		if frag.Blob == "" || (len(dependsOn) > 0 && !delta.MatchesAny(frag.Key, dependsOn)) {
			continue
		}
		chars := float64(frag.Bytes)
		if frags.sig != "" {
			chars = frags.chars[delta.FragmentCacheKey(frag.Key, frag.Fingerprint, frags.sig)]
		}
		if chars == 0 {
			continue
		}
		if entries > 0 {
			total += 5 // delta.Compose's "\n---\n" separator
		}
		total += 4 + float64(utf8.RuneCountInString(frag.Key)) + chars // "## <key>\n"
		entries++
	}
	return total
}
