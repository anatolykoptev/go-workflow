package workflow

// ModelPrice is per-1k-tokens USD pricing for an LLM model.
type ModelPrice struct {
	InputUSDPer1K  float64
	OutputUSDPer1K float64
}

// DefaultCostModel maps known model IDs to per-1k-token prices.
// Override via WithCostModel(map[string]ModelPrice{...}).
// Unknown models cost $0 — surfaces no error, just doesn't accumulate USD.
var DefaultCostModel = map[string]ModelPrice{
	"claude-haiku-4-5":          {InputUSDPer1K: 0.001, OutputUSDPer1K: 0.005},
	"claude-haiku-4-5-20251001": {InputUSDPer1K: 0.001, OutputUSDPer1K: 0.005},
	"claude-sonnet-4-6":         {InputUSDPer1K: 0.003, OutputUSDPer1K: 0.015},
	"claude-opus-4-7":           {InputUSDPer1K: 0.015, OutputUSDPer1K: 0.075},
	"claude-opus-4-6":           {InputUSDPer1K: 0.015, OutputUSDPer1K: 0.075},
	"gemini-2.5-flash":          {InputUSDPer1K: 0.0001, OutputUSDPer1K: 0.0004},
	"gemini-2.5-flash-lite":     {InputUSDPer1K: 0.00005, OutputUSDPer1K: 0.0002},
	"gemini-2.5-pro":            {InputUSDPer1K: 0.00125, OutputUSDPer1K: 0.005},
}

// EstimateUSD computes the dollar cost of a single LLM call given a cost model.
// Returns 0 for unknown models (no error — surfaces in metrics as zero rather
// than crashing the workflow).
func EstimateUSD(model string, inputTokens, outputTokens int64, costModel map[string]ModelPrice) float64 {
	p, ok := costModel[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)*p.InputUSDPer1K/1000 +
		float64(outputTokens)*p.OutputUSDPer1K/1000
}
