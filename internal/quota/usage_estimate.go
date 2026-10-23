package quota

import "math"

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// quotaUsedFraction follows the existing credentialViewModels percentage precedence.
func quotaUsedFraction(row QuotaRow) (float64, bool) {
	var percent float64
	switch {
	case row.UsedPercent != nil && finite(*row.UsedPercent):
		percent = *row.UsedPercent
	case row.RemainingFraction != nil && finite(*row.RemainingFraction):
		percent = 100 - *row.RemainingFraction*100
	case row.Used != nil && row.Limit != nil && finite(*row.Used) && finite(*row.Limit) && *row.Limit > 0:
		percent = *row.Used / *row.Limit * 100
	default:
		return 0, false
	}
	return math.Max(0, math.Min(100, percent)) / 100, true
}

// withWindowUsageEstimates is the original frontend estimate, before formatting.
// Zero/full usage and nonpositive token/cost pairs keep the existing fallback.
func withWindowUsageEstimates(response CheckResponse) CheckResponse {
	response.Quota = append([]QuotaRow(nil), response.Quota...)
	for i := range response.Quota {
		row := &response.Quota[i]
		row.WindowUsageEstimateTokens = nil
		row.WindowUsageEstimateCost = nil
		fraction, ok := quotaUsedFraction(*row)
		if !ok || fraction <= 0 || fraction >= 1 || row.WindowUsageTokens == nil || row.WindowUsageCost == nil {
			continue
		}
		if *row.WindowUsageTokens <= 0 || !finite(*row.WindowUsageCost) || *row.WindowUsageCost <= 0 {
			continue
		}
		tokens, cost := float64(*row.WindowUsageTokens)/fraction, *row.WindowUsageCost/fraction
		if finite(tokens) && finite(cost) {
			row.WindowUsageEstimateTokens = &tokens
			row.WindowUsageEstimateCost = &cost
		}
	}
	return response
}
