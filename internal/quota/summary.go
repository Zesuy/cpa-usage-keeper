package quota

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/timeutil"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm/clause"
)

const SummaryMaxSnapshotAge = 15 * time.Minute

// SummaryCoverage counts files, never deduplicating email addresses or workspaces.
type SummaryCoverage struct {
	Eligible         int        `json:"eligible"`
	Current          int        `json:"current"`
	Direct           int        `json:"direct"`
	Historical       int        `json:"historical"`
	Peer             int        `json:"peer"`
	Unknown          int        `json:"unknown"`
	Missing          int        `json:"missing"`
	Stale            int        `json:"stale"`
	Failed           int        `json:"failed"`
	Pending          int        `json:"pending"`
	Unsupported      int        `json:"unsupported"`
	Samples          int        `json:"samples"`
	OldestEstimateAt *time.Time `json:"oldest_estimate_at,omitempty"`
}

type SummaryRow struct {
	QuotaRow
	Coverage SummaryCoverage `json:"coverage"`
}

type SummaryGroup struct {
	Provider string       `json:"provider"`
	Pool     string       `json:"pool"`
	Label    string       `json:"label"`
	Quota    []SummaryRow `json:"quota"`
}

type SummaryResponse struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Groups      []SummaryGroup `json:"groups"`
	Pending     int            `json:"pending"`
	Eligible    int            `json:"eligible"`
}

type summaryPool struct{ provider, key, label string }

var summaryPools = []summaryPool{
	{"codex", "codex", "Codex / GPT"},
	{"antigravity", antigravityGeminiGroupKey, "Gemini Models"},
	{"antigravity", antigravityClaudeGPTGroupKey, "Claude and GPT models"},
}

func summaryProvider(identity entities.UsageIdentity) string {
	provider := normalizeIdentityType(identity.Type)
	if provider == "" {
		provider = normalizeIdentityType(identity.Provider)
	}
	return provider
}

func summaryPoolForRow(provider string, row QuotaRow) string {
	if provider == "codex" && row.Scope == "window" && (row.Key == "rate_limit.primary_window" || row.Key == "rate_limit.secondary_window") {
		return "codex"
	}
	if provider != "antigravity" || row.Scope != "quota_group" {
		return ""
	}
	// Argon currently shares the Gemini server key: exclude by title before key matching.
	for _, word := range strings.FieldsFunc(strings.ToLower(row.GroupLabel), func(r rune) bool { return r < 'a' || r > 'z' }) {
		if word == "argon" {
			return ""
		}
	}
	switch row.GroupKey {
	case antigravityGeminiGroupKey, antigravityClaudeGPTGroupKey:
		return row.GroupKey
	}
	return ""
}

func summaryPlan(identity entities.UsageIdentity, response *CheckResponse) string {
	provider := summaryProvider(identity)
	if response != nil && response.Subscription != nil && normalizeIdentityType(response.Subscription.Provider) == provider {
		subscription := response.Subscription
		if tier := strings.ToLower(strings.TrimSpace(subscription.TierID)); tier != "" {
			return "tier:" + tier
		}
		if plan := strings.ToLower(strings.TrimSpace(subscription.Plan)); plan != "" && plan != "unknown" {
			return "plan:" + plan
		}
	}
	if provider == "codex" && identity.PlanType != nil {
		if plan := strings.ToLower(strings.TrimSpace(*identity.PlanType)); plan != "" && plan != "unknown" {
			return "plan:" + plan
		}
	}
	return ""
}

func (s *Service) listSummaryCredentials(ctx context.Context, now time.Time) ([]entities.UsageIdentity, error) {
	var identities []entities.UsageIdentity
	err := s.db.WithContext(ctx).
		Select("identity, type, provider, auth_type, is_deleted, disabled, active_start, active_until, plan_type").
		Where("auth_type = ? AND is_deleted = ? AND (disabled IS NULL OR disabled = ?)", entities.UsageIdentityAuthTypeCredential, false, false).
		Order("identity ASC").Find(&identities).Error
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	result := make([]entities.UsageIdentity, 0, len(identities))
	for _, identity := range identities {
		identity.Identity = strings.TrimSpace(identity.Identity)
		provider := summaryProvider(identity)
		if identity.Identity == "" || seen[identity.Identity] || (provider != "codex" && provider != "antigravity") {
			continue
		}
		if identity.ActiveStart != nil && now.Before(*identity.ActiveStart) {
			continue
		}
		if identity.ActiveUntil != nil && !now.Before(*identity.ActiveUntil) {
			continue
		}
		seen[identity.Identity] = true
		result = append(result, identity)
	}
	return result, nil
}

// Called only by successful publication, under estimateMu, never by a summary read.
func (s *Service) rememberCapacityEstimates(authIndex, provider string, response CheckResponse, observedAt time.Time) {
	if s.db == nil || (normalizeIdentityType(provider) != "codex" && normalizeIdentityType(provider) != "antigravity") {
		return
	}
	if s.capacityHistoryInvalid {
		if err := s.db.Where("1 = 1").Delete(&entities.QuotaCapacityEstimate{}).Error; err != nil {
			logrus.WithError(err).Warn("quota capacity estimates invalidation retry failed")
			return
		}
		s.capacityHistoryInvalid = false
	}
	var values []entities.QuotaCapacityEstimate
	for _, row := range response.Quota {
		pool := summaryPoolForRow(normalizeIdentityType(provider), row)
		if pool == "" || row.Window == nil || row.Window.Seconds == nil || (*row.Window.Seconds != quotaWindowFiveHourSeconds && *row.Window.Seconds != quotaWindowSevenDaySeconds) || row.WindowUsageEstimateTokens == nil || row.WindowUsageEstimateCost == nil {
			continue
		}
		rowObservedAt := observedAt
		if row.ObservedAt != nil {
			rowObservedAt = *row.ObservedAt
		}
		values = append(values, entities.QuotaCapacityEstimate{AuthIndex: authIndex, Provider: normalizeIdentityType(provider), Pool: pool, WindowSeconds: *row.Window.Seconds, Tokens: *row.WindowUsageEstimateTokens, CostUSD: *row.WindowUsageEstimateCost, ObservedAt: rowObservedAt})
	}
	if len(values) == 0 {
		return
	}
	var identity entities.UsageIdentity
	if err := s.db.Select("identity, type, provider, plan_type").Where("identity = ? AND auth_type = ? AND is_deleted = ?", authIndex, entities.UsageIdentityAuthTypeCredential, false).First(&identity).Error; err != nil {
		return
	}
	if summaryProvider(identity) != normalizeIdentityType(provider) {
		return
	}
	for i := range values {
		values[i].PlanKey = summaryPlan(identity, &response)
	}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "auth_index"}, {Name: "provider"}, {Name: "pool"}, {Name: "window_seconds"}},
		DoUpdates: clause.AssignmentColumns([]string{"plan_key", "tokens", "cost_usd", "observed_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "excluded.observed_at >= quota_capacity_estimates.observed_at"}}},
	}).Create(&values).Error
	if err != nil {
		logrus.WithError(err).Warn("quota capacity estimate persistence failed")
	}
}

type capacityKey struct {
	auth, provider, pool string
	seconds              int64
}
type cohortKey struct {
	provider, pool, plan string
	seconds              int64
}
type capacityValue struct {
	tokens, cost float64
	at           time.Time
	source       string
}
type summaryCandidate struct {
	row      QuotaRow
	plan     string
	capacity *capacityValue
	fraction float64
}

func validCapacity(value capacityValue) bool {
	return finite(value.tokens) && value.tokens > 0 && finite(value.cost) && value.cost > 0
}

func (s *Service) GetQuotaSummary(ctx context.Context) (SummaryResponse, error) {
	return s.quotaSummaryAt(ctx, time.Now())
}

func (s *Service) quotaSummaryAt(ctx context.Context, now time.Time) (SummaryResponse, error) {
	now = timeutil.NormalizeStorageTime(now)
	response := SummaryResponse{GeneratedAt: now, Groups: []SummaryGroup{}}
	identities, err := s.listSummaryCredentials(ctx, now)
	if err != nil {
		return response, err
	}
	response.Eligible = len(identities)
	if len(identities) == 0 {
		return response, nil
	}
	// Serialize with publications/invalidation, but never hold refreshMu while accessing SQLite.
	s.estimateMu.Lock()
	defer s.estimateMu.Unlock()
	tasks := make(map[string]RefreshTaskRecord, len(identities))
	s.refreshMu.Lock()
	for _, identity := range identities {
		if task := s.refreshTasks[identity.Identity]; task != nil {
			tasks[identity.Identity] = *task
		}
	}
	s.refreshMu.Unlock()
	authIndexes := make([]string, 0, len(identities))
	for _, identity := range identities {
		authIndexes = append(authIndexes, identity.Identity)
	}
	var history []entities.QuotaCapacityEstimate
	if !s.capacityHistoryInvalid {
		if err := s.db.WithContext(ctx).Where("auth_index IN ?", authIndexes).Find(&history).Error; err != nil {
			return response, err
		}
	}
	histories := make(map[capacityKey]entities.QuotaCapacityEstimate, len(history))
	for _, value := range history {
		histories[capacityKey{value.AuthIndex, value.Provider, value.Pool, value.WindowSeconds}] = value
	}
	for _, task := range tasks {
		if task.isActive() {
			response.Pending++
		}
	}
	for _, pool := range summaryPools {
		group := SummaryGroup{Provider: pool.provider, Pool: pool.key, Label: pool.label, Quota: []SummaryRow{}}
		for _, seconds := range []int64{quotaWindowFiveHourSeconds, quotaWindowSevenDaySeconds} {
			label := "5h"
			if seconds == quotaWindowSevenDaySeconds {
				label = "Weekly"
			}
			row := SummaryRow{QuotaRow: QuotaRow{Key: fmt.Sprintf("summary.%s.%d", pool.key, seconds), Label: label, Scope: "window", Window: &QuotaWindow{Seconds: &seconds}}}
			if pool.provider == "antigravity" {
				row.Scope = "quota_group"
				row.GroupKey = pool.key
				row.GroupLabel = pool.label
			}
			candidates := []summaryCandidate{}
			cohorts := make(map[cohortKey][]capacityValue)
			for _, identity := range identities {
				if summaryProvider(identity) != pool.provider {
					continue
				}
				row.Coverage.Eligible++
				task, exists := tasks[identity.Identity]
				switch {
				case !exists:
					row.Coverage.Missing++
					continue
				case task.isActive():
					row.Coverage.Pending++
					continue
				case task.Status != RefreshTaskStatusCompleted || task.Quota == nil:
					row.Coverage.Failed++
					continue
				case task.Type != "" && normalizeIdentityType(task.Type) != pool.provider:
					row.Coverage.Missing++
					continue
				case task.RefreshedAt.IsZero() || now.Sub(task.RefreshedAt) > SummaryMaxSnapshotAge || task.RefreshedAt.After(now):
					row.Coverage.Stale++
					continue
				}
				// A window can appear only once per file/pool. Ambiguous duplicate rows are not summed.
				var matches []QuotaRow
				for _, quotaRow := range task.Quota.Quota {
					if quotaRow.Window != nil && quotaRow.Window.Seconds != nil && *quotaRow.Window.Seconds == seconds && summaryPoolForRow(pool.provider, quotaRow) == pool.key {
						matches = append(matches, quotaRow)
					}
				}
				if len(matches) == 0 {
					row.Coverage.Unsupported++
					continue
				}
				if len(matches) != 1 {
					row.Coverage.Unknown++
					continue
				}
				source := matches[0]
				observedAt := task.RefreshedAt
				if source.ObservedAt != nil {
					observedAt = *source.ObservedAt
				}
				if observedAt.IsZero() || now.Sub(observedAt) > SummaryMaxSnapshotAge || observedAt.After(now) {
					row.Coverage.Stale++
					continue
				}
				reset, resetOK := summaryResetAt(source, observedAt)
				if !resetOK || !now.Before(reset) {
					row.Coverage.Stale++
					continue
				}
				fraction, ok := quotaUsedFraction(source)
				if !ok {
					row.Coverage.Unknown++
					continue
				}
				candidate := summaryCandidate{row: source, plan: summaryPlan(identity, task.Quota), fraction: fraction}
				if source.WindowUsageEstimateTokens != nil && source.WindowUsageEstimateCost != nil && validCapacity(capacityValue{tokens: *source.WindowUsageEstimateTokens, cost: *source.WindowUsageEstimateCost}) {
					candidate.capacity = &capacityValue{*source.WindowUsageEstimateTokens, *source.WindowUsageEstimateCost, observedAt, "direct"}
				} else if old, ok := histories[capacityKey{identity.Identity, pool.provider, pool.key, seconds}]; ok && old.PlanKey == candidate.plan && !old.ObservedAt.After(now) {
					value := capacityValue{old.Tokens, old.CostUSD, old.ObservedAt, "historical"}
					if validCapacity(value) {
						candidate.capacity = &value
					}
				}
				if candidate.capacity != nil && candidate.plan != "" {
					key := cohortKey{pool.provider, pool.key, candidate.plan, seconds}
					cohorts[key] = append(cohorts[key], *candidate.capacity)
				}
				candidates = append(candidates, candidate)
			}
			var currentTokens int64
			var currentCost, totalTokens, totalCost, remainingTokens float64
			usedCohorts := make(map[cohortKey]bool)
			for _, candidate := range candidates {
				source := candidate.row
				if source.WindowUsageTokens != nil && source.WindowUsageCost != nil && *source.WindowUsageTokens >= 0 && finite(*source.WindowUsageCost) && *source.WindowUsageCost >= 0 {
					if currentTokens > math.MaxInt64-*source.WindowUsageTokens {
						return response, fmt.Errorf("quota summary token total overflow")
					}
					currentTokens += *source.WindowUsageTokens
					currentCost += *source.WindowUsageCost
					row.Coverage.Current++
				}
				value := candidate.capacity
				if value == nil && candidate.plan != "" {
					key := cohortKey{pool.provider, pool.key, candidate.plan, seconds}
					samples := cohorts[key]
					if len(samples) > 0 {
						mean := capacityValue{source: "peer", at: now}
						for _, sample := range samples {
							mean.tokens += sample.tokens / float64(len(samples))
							mean.cost += sample.cost / float64(len(samples))
							if sample.at.Before(mean.at) {
								mean.at = sample.at
							}
						}
						if validCapacity(mean) {
							value = &mean
							if !usedCohorts[key] {
								row.Coverage.Samples += len(samples)
								usedCohorts[key] = true
							}
						}
					}
				}
				if value == nil {
					row.Coverage.Unknown++
					continue
				}
				switch value.source {
				case "direct":
					row.Coverage.Direct++
				case "historical":
					row.Coverage.Historical++
				case "peer":
					row.Coverage.Peer++
				}
				if row.Coverage.OldestEstimateAt == nil || value.at.Before(*row.Coverage.OldestEstimateAt) {
					stamp := value.at
					row.Coverage.OldestEstimateAt = &stamp
				}
				totalTokens += value.tokens
				totalCost += value.cost
				remainingTokens += value.tokens * (1 - candidate.fraction)
			}
			if !finite(currentCost) || !finite(totalTokens) || !finite(totalCost) || !finite(remainingTokens) {
				return response, fmt.Errorf("quota summary contains non-finite totals")
			}
			if row.Coverage.Current > 0 {
				row.WindowUsageTokens = &currentTokens
				row.WindowUsageCost = &currentCost
			}
			if totalTokens > 0 {
				row.WindowUsageEstimateTokens = &totalTokens
				row.WindowUsageEstimateCost = &totalCost
				remaining := math.Max(0, math.Min(1, remainingTokens/totalTokens))
				row.RemainingFraction = &remaining
			}
			if row.Coverage.Eligible > 0 {
				group.Quota = append(group.Quota, row)
			}
		}
		if len(group.Quota) > 0 {
			response.Groups = append(response.Groups, group)
		}
	}
	return response, nil
}

func summaryResetAt(row QuotaRow, refreshedAt time.Time) (time.Time, bool) {
	if row.ResetAt != "" {
		parsed, err := timeutil.ParseStorageTime(row.ResetAt)
		return parsed, err == nil
	}
	if row.ResetAfterSeconds != nil && *row.ResetAfterSeconds >= 0 {
		seconds := *row.ResetAfterSeconds
		if seconds <= int64((1<<63-1)/int64(time.Second)) {
			return refreshedAt.Add(time.Duration(seconds) * time.Second), true
		}
	}
	return time.Time{}, false
}

func (s *Service) RefreshQuotaSummary(ctx context.Context) (RefreshResponse, error) {
	identities, err := s.listSummaryCredentials(ctx, time.Now())
	if err != nil {
		return RefreshResponse{}, err
	}
	indexes := make([]string, 0, len(identities))
	for _, identity := range identities {
		indexes = append(indexes, identity.Identity)
	}
	sort.Strings(indexes)
	if len(indexes) == 0 {
		return RefreshResponse{Tasks: []RefreshTaskRef{}, Rejected: []RefreshRejectedAuthIndex{}}, nil
	}
	return s.Refresh(ctx, RefreshRequest{AuthIndexes: indexes, Source: RefreshSourceManual})
}
