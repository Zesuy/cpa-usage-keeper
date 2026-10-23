package quota

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/migration"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Opt in with a completed offline backup. Never opens the supplied file for writing,
// never runs the app/collector, and never prints identifiers or request contents.
func TestQuotaSummaryFromProductionBackup(t *testing.T) {
	source := os.Getenv("KEEPER_SUMMARY_TEST_BACKUP")
	if source == "" {
		t.Skip("set KEEPER_SUMMARY_TEST_BACKUP to an offline SQLite backup")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "production-copy.db")
	if err = os.WriteFile(target, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(target), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	// All migrations operate on the disposable local copy only.
	if err = migration.Run(db, migration.RunOptions{BeforeDestructiveMigration: func(context.Context, string) error {
		return fmt.Errorf("unexpected destructive migration in offline fixture")
	}}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&entities.QuotaCapacityEstimate{}) {
		t.Fatal("capacity table missing")
	}
	// A second startup is idempotent and leaves the imported usage intact.
	var eventsBefore, eventsAfter int64
	db.Model(&entities.UsageEvent{}).Count(&eventsBefore)
	if err = migration.Run(db); err != nil {
		t.Fatal(err)
	}
	db.Model(&entities.UsageEvent{}).Count(&eventsAfter)
	if eventsBefore != eventsAfter {
		t.Fatal("migration changed stored usage")
	}
	var identities []entities.UsageIdentity
	if err = db.Where("auth_type = ? AND type = ? AND is_deleted = ? AND COALESCE(disabled,0) = 0", 1, "codex", false).Find(&identities).Error; err != nil {
		t.Fatal(err)
	}
	s := summaryTestService(db)
	var cycleRows int
	var numericMatches int
	var generatedAt time.Time
	for _, identity := range identities {
		var cycles []entities.QuotaCycle
		if err = db.Where("auth_index = ? AND provider = ?", identity.Identity, "codex").Order("last_observed_at DESC").Find(&cycles).Error; err != nil {
			t.Fatal("read cycle fixture")
		}
		selected := map[int64]bool{}
		var quotaRows []QuotaRow
		var refreshed time.Time
		calculator, err := repository.NewUsageWindowStatsCalculator(db)
		if err != nil {
			t.Fatal(err)
		}
		for _, cycle := range cycles {
			if selected[cycle.WindowSeconds] || (cycle.WindowSeconds != 18000 && cycle.WindowSeconds != 604800) {
				continue
			}
			selected[cycle.WindowSeconds] = true
			var segment entities.QuotaPercentSegment
			if err = db.Where("cycle_id = ?", cycle.ID).Order("last_observed_at DESC").First(&segment).Error; err != nil {
				t.Fatal("read percent fixture")
			}
			at := cycle.LastObservedAt
			start := cycle.ResetAt.Add(-time.Duration(cycle.WindowSeconds) * time.Second)
			stats, err := calculator.SumByAuthIndex(context.Background(), identity.Identity, start, &at)
			if err != nil {
				t.Fatal("window stats fixture unavailable")
			}
			remaining := float64(segment.RemainingPercent) / 100
			seconds := cycle.WindowSeconds
			row := QuotaRow{Key: cycle.QuotaKey, Scope: "window", Window: &QuotaWindow{Seconds: &seconds}, RemainingFraction: &remaining, ResetAt: timeutil.NormalizeStorageTime(cycle.ResetAt).Format(time.RFC3339Nano), WindowUsageTokens: &stats.Tokens, WindowUsageCost: &stats.Cost, ObservedAt: &at}
			response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{row}})
			row = response.Quota[0]
			if remaining > 0 && remaining < 1 && stats.Tokens > 0 && stats.Cost > 0 {
				// Same arithmetic and unclipped sample policy as the old credential UI.
				assertNear(t, row.WindowUsageEstimateTokens, float64(stats.Tokens)/(1-remaining))
				assertNear(t, row.WindowUsageEstimateCost, stats.Cost/(1-remaining))
				numericMatches++
			}
			quotaRows = append(quotaRows, row)
			if at.After(refreshed) {
				refreshed = at
			}
			if at.After(generatedAt) {
				generatedAt = at
			}
			cycleRows++
		}
		summaryTestCache(s, identity, quotaRows, refreshed)
	}
	if cycleRows == 0 || numericMatches == 0 {
		t.Fatal("backup has no usable numeric cycle fixtures")
	}
	result, err := s.quotaSummaryAt(context.Background(), generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) == 0 {
		t.Fatal("missing aggregate groups")
	}
	var expectedStats SummaryUsageStats
	for _, identity := range identities {
		if identity.ActiveStart != nil && generatedAt.Before(*identity.ActiveStart) || identity.ActiveUntil != nil && !generatedAt.Before(*identity.ActiveUntil) {
			continue
		}
		expectedStats.TotalRequests += identity.TotalRequests - identity.ResetTotalRequests
		expectedStats.SuccessCount += identity.SuccessCount - identity.ResetSuccessCount
		expectedStats.FailureCount += identity.FailureCount - identity.ResetFailureCount
		expectedStats.InputTokens += identity.InputTokens - identity.ResetInputTokens
		expectedStats.CacheReadTokens += identity.CacheReadTokens - identity.ResetCacheReadTokens
		expectedStats.TotalTokens += identity.TotalTokens - identity.ResetTotalTokens
	}
	if result.Groups[0].Stats != expectedStats {
		t.Fatal("summary metrics differ from stored credential counters")
	}
	for _, aggregate := range result.Groups[0].Quota {
		var expectedTokens int64
		var expectedCost float64
		for _, task := range s.refreshTasks {
			for _, row := range task.Quota.Quota {
				if *row.Window.Seconds != *aggregate.Window.Seconds || generatedAt.Sub(*row.ObservedAt) > SummaryMaxSnapshotAge {
					continue
				}
				reset, ok := summaryResetAt(row, *row.ObservedAt)
				if !ok || !generatedAt.Before(reset) {
					continue
				}
				expectedTokens += *row.WindowUsageTokens
				expectedCost += *row.WindowUsageCost
			}
		}
		if aggregate.WindowUsageTokens == nil || *aggregate.WindowUsageTokens != expectedTokens {
			t.Fatal("aggregate tokens differ from stored window usage")
		}
		assertNear(t, aggregate.WindowUsageCost, expectedCost)
	}
	t.Logf("offline backup migrated; credentials=%d reconstructed_windows=%d unchanged_estimates=%d groups=%d", len(identities), cycleRows, numericMatches, len(result.Groups))
}
