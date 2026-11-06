// Stage-specific pricing keeps a view role's budget and rates independent of fragments.
package pipeline

func tokensForChars(chars int) int {
	return chars / charsPerToken
}

func (rc RunConfig) viewPricing() Pricing {
	if rc.ViewPricing != nil {
		return *rc.ViewPricing
	}
	return rc.Pricing
}

func (p Pricing) cost(input, output float64) float64 {
	return input/1e6*p.PerMTokIn + output/1e6*p.PerMTokOut
}

func (e *Estimate) addTokens(input int, pricing Pricing) {
	e.InputTokens += input
	e.OutputTokens += pricing.MaxOutputTokens
	e.CostUSD += pricing.cost(float64(input), float64(pricing.MaxOutputTokens))
}
