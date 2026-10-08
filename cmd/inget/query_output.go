// Human-readable query output. Scores remain vector similarities even when optional
// LLM ranking changes result order; #N marks a successful rerank position.
package main

import (
	"fmt"
	"strings"
)

// printHits writes the readable rendering: one header line per hit, then a snippet.
func printHits(hits []hit) {
	if len(hits) == 0 {
		fmt.Println("no hits")
		return
	}
	for _, h := range hits {
		if h.Rank > 0 {
			fmt.Printf("#%d  ", h.Rank)
		}
		fmt.Printf("%.4f  %s  %s  [%s]\n", h.Score, h.Datatype, h.ItemID, h.ViewName)
		fmt.Printf("        %s\n", snippet(h.Text))
	}
}

// snippet renders a view's text as one line, bounded so a screenful of hits stays readable.
func snippet(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	if len(flat) <= snippetChars {
		return flat
	}
	return flat[:snippetChars] + "…"
}
