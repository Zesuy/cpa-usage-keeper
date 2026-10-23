package quota

import (
	"context"
	"encoding/json"
	"time"

	"cpa-usage-keeper/internal/entities"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A successful publication is authoritative even if its database write fails.
// Partial publications mask incompatible plans; full publications also mask
// windows omitted by the latest response. Empty payloads mark ambiguous rows.
type summarySnapshotScope struct {
	provider, plan string
	complete       bool
}

// All methods here run under estimateMu, outside refreshMu. Snapshot persistence
// is best effort; the last successful observation remains usable in this process.
func (s *Service) rememberSummarySnapshot(auth, provider string, response CheckResponse, at time.Time, replace bool) {
	provider = normalizeIdentityType(provider)
	if s.db == nil || (provider != "codex" && provider != "antigravity") {
		return
	}
	var identity entities.UsageIdentity
	if err := s.db.Select("identity, type, provider, plan_type").Where("identity = ? AND auth_type = ? AND is_deleted = ?", auth, entities.UsageIdentityAuthTypeCredential, false).First(&identity).Error; err != nil || summaryProvider(identity) != provider {
		return
	}
	s.rememberSummarySnapshotForIdentity(identity, response, at, replace)
}

// Header batches already resolved their identities; reuse that lookup rather
// than adding an identity query for every published window.
func (s *Service) rememberSummarySnapshotForIdentity(identity entities.UsageIdentity, response CheckResponse, at time.Time, replace bool) {
	provider := summaryProvider(identity)
	if s.db == nil || (provider != "codex" && provider != "antigravity") {
		return
	}
	auth := identity.Identity
	plan := summaryPlan(identity, &response)
	values := []entities.QuotaSummarySnapshot{}
	for _, row := range response.Quota {
		pool := summaryPoolForRow(provider, row)
		if pool == "" || row.Window == nil || row.Window.Seconds == nil || (*row.Window.Seconds != quotaWindowFiveHourSeconds && *row.Window.Seconds != quotaWindowSevenDaySeconds) {
			continue
		}
		observed := at
		if row.ObservedAt != nil {
			observed = *row.ObservedAt
		}
		// Whitelist normalized fields used by the summary, excluding all raw output,
		// subscription/account metadata, file names and unrelated quota features.
		clean := QuotaRow{
			Key: row.Key, Label: row.Label, Scope: row.Scope,
			GroupKey: row.GroupKey, GroupLabel: row.GroupLabel, Window: row.Window,
			Used: row.Used, Limit: row.Limit, UsedPercent: row.UsedPercent,
			RemainingFraction: row.RemainingFraction,
			ResetAt:           row.ResetAt, ResetAfterSeconds: row.ResetAfterSeconds,
			WindowUsageTokens: row.WindowUsageTokens, WindowUsageCost: row.WindowUsageCost,
			WindowUsageEstimateTokens: row.WindowUsageEstimateTokens,
			WindowUsageEstimateCost:   row.WindowUsageEstimateCost, ObservedAt: &observed,
		}
		payload, err := json.Marshal(clean)
		if err != nil {
			logrus.Warn("quota summary snapshot serialization failed")
			continue
		}
		values = append(values, entities.QuotaSummarySnapshot{AuthIndex: auth, Provider: provider, Pool: pool, WindowSeconds: *row.Window.Seconds, PlanKey: plan, IdentityPlanKey: summaryPlan(identity, nil), Payload: string(payload), ObservedAt: observed})
	}
	counts := make(map[capacityKey]int)
	for _, value := range values {
		counts[capacityKey{auth, provider, value.Pool, value.WindowSeconds}]++
	}
	unique := values[:0]
	for _, value := range values {
		if counts[capacityKey{auth, provider, value.Pool, value.WindowSeconds}] == 1 {
			unique = append(unique, value)
		}
	}
	values = unique
	if s.summarySnapshots == nil {
		s.summarySnapshots = make(map[capacityKey]entities.QuotaSummarySnapshot)
	}
	if s.summarySnapshotScopes == nil {
		s.summarySnapshotScopes = make(map[string]summarySnapshotScope)
	}
	previous := s.summarySnapshotScopes[auth]
	s.summarySnapshotScopes[auth] = summarySnapshotScope{provider, plan, replace || previous.complete}
	for key, old := range s.summarySnapshots {
		if key.auth == auth && (replace || old.Provider != provider || old.PlanKey != plan) {
			delete(s.summarySnapshots, key)
		}
	}
	for _, value := range values {
		key := capacityKey{auth, provider, value.Pool, value.WindowSeconds}
		if old, ok := s.summarySnapshots[key]; !ok || !old.ObservedAt.After(value.ObservedAt) {
			s.summarySnapshots[key] = value
		}
	}
	for key, count := range counts {
		if count > 1 {
			s.summarySnapshots[key] = entities.QuotaSummarySnapshot{Provider: provider, PlanKey: plan}
		}
	}
	if s.capacityHistoryInvalid {
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("auth_index = ?", auth)
		if !replace {
			query = query.Where("provider <> ? OR plan_key <> ?", provider, plan)
		}
		if err := query.Delete(&entities.QuotaSummarySnapshot{}).Error; err != nil {
			return err
		}
		if !replace {
			for key, count := range counts {
				if count > 1 {
					if err := tx.Where("auth_index = ? AND provider = ? AND pool = ? AND window_seconds = ?", auth, provider, key.pool, key.seconds).Delete(&entities.QuotaSummarySnapshot{}).Error; err != nil {
						return err
					}
				}
			}
		}
		if len(values) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "auth_index"}, {Name: "provider"}, {Name: "pool"}, {Name: "window_seconds"}},
			DoUpdates: clause.AssignmentColumns([]string{"plan_key", "identity_plan_key", "payload", "observed_at"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "excluded.observed_at >= quota_summary_snapshots.observed_at"}}},
		}).Create(&values).Error
	})
	if err != nil {
		logrus.WithError(err).Warn("quota summary snapshot persistence failed")
	}
}

func (s *Service) loadSummarySnapshots(ctx context.Context, indexes []string) (map[capacityKey]entities.QuotaSummarySnapshot, error) {
	result := make(map[capacityKey]entities.QuotaSummarySnapshot)
	if !s.capacityHistoryInvalid {
		var stored []entities.QuotaSummarySnapshot
		if err := s.db.WithContext(ctx).Where("auth_index IN ?", indexes).Find(&stored).Error; err != nil {
			return nil, err
		}
		for _, value := range stored {
			if scope, ok := s.summarySnapshotScopes[value.AuthIndex]; ok && (scope.complete || scope.provider != value.Provider || scope.plan != value.PlanKey) {
				continue
			}
			result[capacityKey{value.AuthIndex, value.Provider, value.Pool, value.WindowSeconds}] = value
		}
	}
	for key, value := range s.summarySnapshots {
		if value.Payload == "" {
			delete(result, key)
			continue
		}
		if old, ok := result[key]; !ok || !old.ObservedAt.After(value.ObservedAt) {
			result[key] = value
		}
	}
	return result, nil
}

func (s *Service) clearStoredQuotaSummary() error {
	if s.db == nil {
		return nil
	}
	// Older embedding/test databases may not have installed the additive migration.
	hasCapacity := s.db.Migrator().HasTable(&entities.QuotaCapacityEstimate{})
	hasSnapshots := s.db.Migrator().HasTable(&entities.QuotaSummarySnapshot{})
	return s.db.Transaction(func(tx *gorm.DB) error {
		if hasCapacity {
			if err := tx.Where("1 = 1").Delete(&entities.QuotaCapacityEstimate{}).Error; err != nil {
				return err
			}
		}
		if hasSnapshots {
			return tx.Where("1 = 1").Delete(&entities.QuotaSummarySnapshot{}).Error
		}
		return nil
	})
}
