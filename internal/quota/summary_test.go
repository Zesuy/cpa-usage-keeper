package quota

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func ptr[T any](value T) *T { return &value }
func summaryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "summary.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&entities.UsageIdentity{}, &entities.QuotaCapacityEstimate{}, &entities.QuotaSummarySnapshot{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	return db
}
func summaryTestService(db *gorm.DB) *Service {
	return &Service{db: db, refreshTasks: map[string]*RefreshTaskRecord{}}
}
func summaryTestIdentity(t *testing.T, db *gorm.DB, auth, provider, plan string) entities.UsageIdentity {
	t.Helper()
	identity := entities.UsageIdentity{Identity: auth, Type: provider, Provider: provider, AuthType: entities.UsageIdentityAuthTypeCredential, PlanType: &plan}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	return identity
}
func summaryTestRow(seconds int64, used float64, tokens int64, cost float64, now time.Time) QuotaRow {
	key := "rate_limit.primary_window"
	if seconds == quotaWindowSevenDaySeconds {
		key = "rate_limit.secondary_window"
	}
	return QuotaRow{Key: key, Scope: "window", Window: &QuotaWindow{Seconds: &seconds}, UsedPercent: &used, WindowUsageTokens: &tokens, WindowUsageCost: &cost, ResetAt: now.Add(time.Hour).Format(time.RFC3339)}
}
func summaryTestCache(s *Service, identity entities.UsageIdentity, rows []QuotaRow, now time.Time) {
	s.refreshTasks[identity.Identity] = &RefreshTaskRecord{AuthIndex: identity.Identity, Type: identity.Type, Status: RefreshTaskStatusCompleted, RefreshedAt: now, Quota: ptr(withWindowUsageEstimates(CheckResponse{ID: identity.Identity, Quota: rows, Subscription: &SubscriptionInfo{Provider: identity.Type, Plan: *identity.PlanType}}))}
}
func assertNear(t *testing.T, actual *float64, expected float64) {
	t.Helper()
	if actual == nil || math.Abs(*actual-expected) > math.Max(1e-8, math.Abs(expected)*1e-12) {
		t.Fatalf("got %v expected %v", actual, expected)
	}
}
func TestWindowEstimateMatchesExistingFrontendRules(t *testing.T) {
	cases := []struct {
		name         string
		row          QuotaRow
		tokens, cost float64
		valid        bool
	}{
		{"used precedence", QuotaRow{UsedPercent: ptr(25.0), RemainingFraction: ptr(.1), WindowUsageTokens: ptr(int64(1_000_000)), WindowUsageCost: ptr(2.5)}, 4_000_000, 10, true},
		{"remaining", QuotaRow{RemainingFraction: ptr(.75), WindowUsageTokens: ptr(int64(500_000)), WindowUsageCost: ptr(1.0)}, 2_000_000, 4, true},
		{"used limit", QuotaRow{Used: ptr(2.0), Limit: ptr(10.0), WindowUsageTokens: ptr(int64(100)), WindowUsageCost: ptr(1.0)}, 500, 5, true},
		{"zero", QuotaRow{UsedPercent: ptr(0.0), WindowUsageTokens: ptr(int64(100)), WindowUsageCost: ptr(1.0)}, 0, 0, false},
		{"full", QuotaRow{UsedPercent: ptr(100.0), WindowUsageTokens: ptr(int64(100)), WindowUsageCost: ptr(1.0)}, 0, 0, false},
		{"free", QuotaRow{UsedPercent: ptr(50.0), WindowUsageTokens: ptr(int64(100)), WindowUsageCost: ptr(0.0)}, 0, 0, false},
		{"nonfinite", QuotaRow{UsedPercent: ptr(50.0), WindowUsageTokens: ptr(int64(100)), WindowUsageCost: ptr(math.Inf(1))}, 0, 0, false},
		{"low percent preserved", QuotaRow{UsedPercent: ptr(1.0), WindowUsageTokens: ptr(int64(100)), WindowUsageCost: ptr(1.0)}, 10_000, 100, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := CheckResponse{Quota: []QuotaRow{tc.row}}
			got := withWindowUsageEstimates(input).Quota[0]
			if !tc.valid {
				if got.WindowUsageEstimateTokens != nil || got.WindowUsageEstimateCost != nil {
					t.Fatal("invalid estimate accepted")
				}
				return
			}
			assertNear(t, got.WindowUsageEstimateTokens, tc.tokens)
			assertNear(t, got.WindowUsageEstimateCost, tc.cost)
			if input.Quota[0].WindowUsageEstimateCost != nil {
				t.Fatal("input snapshot mutated")
			}
		})
	}
}

func TestSummaryUsesCurrentThenOwnHistoryThenCohortArithmeticMean(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	a := summaryTestIdentity(t, db, "same-email-workspace-a", "codex", "team")
	b := summaryTestIdentity(t, db, "same-email-workspace-b", "codex", "team")
	c := summaryTestIdentity(t, db, "same-email-workspace-c", "codex", "team")
	d := summaryTestIdentity(t, db, "no-team-samples", "codex", "plus")
	summaryTestCache(s, a, []QuotaRow{summaryTestRow(18000, 25, 100, 2.5, now)}, now)
	summaryTestCache(s, b, []QuotaRow{summaryTestRow(18000, 0, 0, 0, now)}, now)
	summaryTestCache(s, c, []QuotaRow{summaryTestRow(18000, 50, 0, 0, now)}, now)
	summaryTestCache(s, d, []QuotaRow{summaryTestRow(18000, 0, 0, 0, now)}, now)
	historical := entities.QuotaCapacityEstimate{AuthIndex: b.Identity, Provider: "codex", Pool: "codex", WindowSeconds: 18000, PlanKey: "plan:team", Tokens: 800, CostUSD: 20, ObservedAt: now.Add(-24 * time.Hour)}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	result, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Eligible != 4 {
		t.Fatal(result.Eligible)
	}
	row := result.Groups[0].Quota[0]
	// A=400, B=800 historical, C=(400+800)/2=600 peer; plus stays unknown.
	assertNear(t, row.WindowUsageEstimateTokens, 1800)
	assertNear(t, row.WindowUsageEstimateCost, 45)
	assertNear(t, row.RemainingFraction, 1400.0/1800)
	if *row.WindowUsageTokens != 100 || *row.WindowUsageCost != 2.5 {
		t.Fatalf("actual usage changed: %+v", row)
	}
	if row.Coverage.Direct != 1 || row.Coverage.Historical != 1 || row.Coverage.Peer != 1 || row.Coverage.Unknown != 1 {
		t.Fatalf("coverage %+v", row.Coverage)
	}
	if row.Coverage.Current != 4 {
		t.Fatal(row.Coverage)
	}
	var count int64
	db.Model(&entities.QuotaCapacityEstimate{}).Count(&count)
	if count != 1 {
		t.Fatal("summary read wrote peer estimates")
	}
}

func TestSummaryHistorySurvivesRestartAndOwnValueWinsOverPeers(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	identity := summaryTestIdentity(t, db, "a", "codex", "team")
	response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 25, 100, 2.5, now)}})
	s.rememberCapacityEstimates(identity.Identity, "codex", response, now.Add(-time.Hour))
	restarted := summaryTestService(db)
	summaryTestCache(restarted, identity, []QuotaRow{summaryTestRow(18000, 0, 0, 0, now)}, now)
	result, err := restarted.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, result.Groups[0].Quota[0].WindowUsageEstimateCost, 10)
	if result.Groups[0].Quota[0].Coverage.Historical != 1 {
		t.Fatal(result)
	}
	restarted.InvalidateStoredCostCache()
	var count int64
	db.Model(&entities.QuotaCapacityEstimate{}).Count(&count)
	if count != 0 {
		t.Fatal("cost invalidation retained estimates")
	}
}

func TestSummarySeparatesWindowsPoolsAndExcludesArgonAndSpecialQuotas(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	codex := summaryTestIdentity(t, db, "codex", "codex", "team")
	five := summaryTestRow(18000, 25, 100, 1, now)
	week := summaryTestRow(604800, 50, 1000, 20, now)
	special := five
	special.Key = "code_review_rate_limit.primary_window"
	special.Scope = "code_review"
	summaryTestCache(s, codex, []QuotaRow{five, week, special}, now)
	ag := summaryTestIdentity(t, db, "ag", "antigravity", "pro")
	gemini := five
	gemini.Scope = "quota_group"
	gemini.GroupKey = antigravityGeminiGroupKey
	gemini.GroupLabel = "Gemini Models"
	gemini.Key = "bucket.gemini.5h"
	claude := gemini
	claude.GroupKey = antigravityClaudeGPTGroupKey
	claude.GroupLabel = "Claude and GPT models"
	claude.Key = "bucket.claude.5h"
	argon := gemini
	argon.GroupLabel = "Gemini 4 Argon"
	argon.Key = "bucket.argon.weekly"
	argon.Window = &QuotaWindow{Seconds: ptr(int64(604800))}
	summaryTestCache(s, ag, []QuotaRow{gemini, claude, argon}, now)
	result, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 3 {
		t.Fatal(result)
	}
	assertNear(t, result.Groups[0].Quota[0].WindowUsageEstimateCost, 4)
	assertNear(t, result.Groups[0].Quota[1].WindowUsageEstimateCost, 40)
	assertNear(t, result.Groups[1].Quota[0].WindowUsageEstimateCost, 4)
	if result.Groups[1].Quota[1].WindowUsageTokens != nil || result.Groups[1].Quota[1].Coverage.Unsupported != 1 {
		t.Fatal("argon counted as Gemini weekly")
	}
}

func TestSummaryExcludesInvalidCredentialsAndClassifiesStaleSnapshots(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	for _, name := range []string{"valid", "old", "reset", "relative", "missing", "failed", "pending", "disabled", "expired", "future", "deleted"} {
		id := summaryTestIdentity(t, db, name, "codex", "team")
		switch name {
		case "disabled":
			db.Model(&id).Update("disabled", true)
		case "expired":
			db.Model(&id).Update("active_until", now.Add(-time.Hour))
		case "future":
			db.Model(&id).Update("active_start", now.Add(time.Hour))
		case "deleted":
			db.Model(&id).Update("is_deleted", true)
		}
		row := summaryTestRow(18000, 25, 100, 1, now)
		at := now
		if name == "old" {
			at = now.Add(-SummaryMaxSnapshotAge - time.Second)
		}
		if name == "reset" {
			row.ResetAt = now.Format(time.RFC3339)
		}
		if name == "relative" {
			row.ResetAt = ""
			row.ResetAfterSeconds = ptr(int64(10))
			at = now.Add(-time.Minute)
		}
		if name == "missing" {
			continue
		}
		summaryTestCache(s, id, []QuotaRow{row}, at)
		if name == "failed" {
			s.refreshTasks[name].Status = RefreshTaskStatusFailed
		}
		if name == "pending" {
			s.refreshTasks[name].Status = RefreshTaskStatusRunning
		}
	}
	result, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	cov := result.Groups[0].Quota[0].Coverage
	if result.Eligible != 7 || result.Pending != 1 || cov.Direct != 2 || cov.Historical != 2 || cov.Stale != 1 || cov.Expired != 2 || cov.Missing != 1 || cov.Failed != 1 || cov.Pending != 1 {
		t.Fatalf("%+v eligible=%d pending=%d", cov, result.Eligible, result.Pending)
	}
}

func TestSummaryPublicationsPersistEstimatesAndRetainRowFreshness(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "published", "codex", "team")
	now := time.Now().Truncate(time.Second)
	old := now.Add(-SummaryMaxSnapshotAge - time.Second)
	five := summaryTestRow(18000, 25, 100, 1, now)
	week := summaryTestRow(604800, 50, 1000, 10, now)
	task := &RefreshTaskRecord{AuthIndex: id.Identity, Type: id.Type, Status: RefreshTaskStatusRunning}
	s.refreshTasks[id.Identity] = task
	s.markRefreshTaskCompleted(task, CheckResponse{Quota: []QuotaRow{five, week}}, nil)
	now = time.Now().Add(time.Second).Truncate(time.Second)
	var values []entities.QuotaCapacityEstimate
	if err := db.Find(&values).Error; err != nil || len(values) != 2 {
		t.Fatalf("own estimates not persisted: %v %d", err, len(values))
	}
	// Make just the weekly observation old, then publish a partial fresh 5h header.
	task.Quota.Quota[1].ObservedAt = &old
	task.RefreshedAt = old
	five.UsedPercent = ptr(50.0)
	five.WindowUsageTokens = ptr(int64(300))
	five.WindowUsageCost = ptr(3.0)
	if !s.mergeUsageHeaderQuotaCache(id.Identity, CheckResponse{Quota: []QuotaRow{five}}, now, id, 0) {
		t.Fatal("header not published")
	}
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateTokens, 600)
	if got.Groups[0].Quota[1].Coverage.Stale != 1 || got.Groups[0].Quota[1].WindowUsageTokens == nil {
		t.Fatal("partial header rejuvenated weekly data")
	}
	var persisted entities.QuotaCapacityEstimate
	if err := db.Where("auth_index = ? AND window_seconds = ?", id.Identity, 18000).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Tokens != 600 {
		t.Fatal("header kept old estimate", persisted.Tokens)
	}
	// A new zero-progress snapshot must clear the direct estimate and use own history.
	five.UsedPercent = ptr(0.0)
	five.WindowUsageTokens = ptr(int64(0))
	five.WindowUsageCost = ptr(0.0)
	later := now.Add(time.Second)
	if !s.mergeUsageHeaderQuotaCache(id.Identity, CheckResponse{Quota: []QuotaRow{five}}, later, id, 0) {
		t.Fatal("second header not published")
	}
	got, err = s.quotaSummaryAt(context.Background(), later)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].Coverage.Historical != 1 {
		t.Fatal("old direct estimate survived header merge")
	}
	assertNear(t, got.Groups[0].Quota[0].RemainingFraction, 1)
	// Invalidated task/header generations can neither publish nor persist.
	s.InvalidateStoredCostCache()
	s.markRefreshTaskCompleted(task, CheckResponse{Quota: []QuotaRow{week}}, nil)
	if s.mergeUsageHeaderQuotaCache(id.Identity, CheckResponse{Quota: []QuotaRow{week}}, later.Add(time.Second), id, 0) {
		t.Fatal("stale generation published")
	}
	var count int64
	db.Model(&entities.QuotaCapacityEstimate{}).Count(&count)
	if count != 0 {
		t.Fatal("stale generation repopulated persisted history")
	}
	db.Model(&entities.QuotaSummarySnapshot{}).Count(&count)
	if count != 0 {
		t.Fatal("stale generation repopulated saved snapshots")
	}
}

func TestSummaryFullQuotaAndPlanChangesDoNotInventCapacity(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	for _, name := range []string{"exhausted", "unknown", "changed"} {
		plan := "team"
		if name == "unknown" {
			plan = ""
		}
		id := summaryTestIdentity(t, db, name, "codex", plan)
		summaryTestCache(s, id, []QuotaRow{summaryTestRow(18000, 100, 400, 4, now)}, now)
		if name != "unknown" {
			oldPlan := "plan:team"
			if name == "changed" {
				oldPlan = "plan:plus"
			}
			if err := db.Create(&entities.QuotaCapacityEstimate{AuthIndex: name, Provider: "codex", Pool: "codex", PlanKey: oldPlan, WindowSeconds: 18000, Tokens: 400, CostUSD: 4, ObservedAt: now.Add(-24 * time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	row := got.Groups[0].Quota[0]
	// Same-plan exhausted peer can use the team sample; unknown plan cannot.
	if row.Coverage.Historical != 1 || row.Coverage.Peer != 1 || row.Coverage.Unknown != 1 || row.Coverage.Samples != 1 {
		t.Fatal(row.Coverage)
	}
	assertNear(t, row.WindowUsageEstimateTokens, 800)
	assertNear(t, row.RemainingFraction, 0)
}

func TestSummaryFailedInvalidationCannotReadOldPrices(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	id := summaryTestIdentity(t, db, "costs", "codex", "team")
	response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 25, 100, 1, now)}})
	s.rememberCapacityEstimates(id.Identity, id.Type, response, now)
	if err := db.Exec("CREATE TRIGGER block_capacity_delete BEFORE DELETE ON quota_capacity_estimates BEGIN SELECT RAISE(ABORT, 'test delete blocked'); END").Error; err != nil {
		t.Fatal(err)
	}
	s.InvalidateStoredCostCache()
	summaryTestCache(s, id, []QuotaRow{summaryTestRow(18000, 0, 0, 0, now)}, now)
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].WindowUsageEstimateCost != nil {
		t.Fatal("old prices reused after failed invalidation")
	}
	if err := db.Exec("DROP TRIGGER block_capacity_delete").Error; err != nil {
		t.Fatal(err)
	}
	s.rememberCapacityEstimates(id.Identity, id.Type, response, now)
	if s.capacityHistoryInvalid {
		t.Fatal("successful retry did not enable fresh history")
	}
}

func TestSummaryRefreshUsesAllEligibleFilesAndReusesPendingTasks(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	for i := 0; i < 27; i++ {
		id := summaryTestIdentity(t, db, fmt.Sprintf("all-pages-%02d", i), "codex", "team")
		s.refreshTasks[id.Identity] = &RefreshTaskRecord{AuthIndex: id.Identity, Status: RefreshTaskStatusRunning}
	}
	disabled := summaryTestIdentity(t, db, "disabled", "antigravity", "pro")
	db.Model(&disabled).Update("disabled", true)
	summaryTestIdentity(t, db, "other-provider", "claude", "pro")
	result, err := s.RefreshQuotaSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Limit != 27 || result.Accepted != 0 || result.Skipped != 27 {
		t.Fatalf("wrong scope or duplicate dispatch: %+v", result)
	}
	for _, rejected := range result.Rejected {
		if rejected.Error != "duplicate" {
			t.Fatal(rejected)
		}
	}
}

func TestSummaryMetricsReuseResetAdjustedCredentialCounters(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	a := summaryTestIdentity(t, db, "metrics-a", "codex", "team")
	a.TotalRequests, a.SuccessCount, a.FailureCount = 10, 9, 1
	a.InputTokens, a.CacheReadTokens, a.TotalTokens = 100, 80, 150
	a.ResetTotalRequests, a.ResetSuccessCount, a.ResetFailureCount = 2, 2, 0
	a.ResetInputTokens, a.ResetCacheReadTokens, a.ResetTotalTokens = 20, 10, 30
	a.StatsResetAt = ptr(now.Add(-time.Hour))
	if err := db.Save(&a).Error; err != nil {
		t.Fatal(err)
	}
	b := summaryTestIdentity(t, db, "metrics-b", "codex", "team")
	b.TotalRequests, b.SuccessCount, b.FailureCount = 90, 45, 45
	b.InputTokens, b.CacheReadTokens, b.TotalTokens = 900, 90, 1000
	if err := db.Save(&b).Error; err != nil {
		t.Fatal(err)
	}
	ag := summaryTestIdentity(t, db, "metrics-ag", "antigravity", "pro")
	ag.TotalRequests, ag.SuccessCount, ag.FailureCount = 6, 4, 2
	ag.InputTokens, ag.CacheReadTokens, ag.TotalTokens = 200, 50, 400
	ag.ResetTotalRequests, ag.ResetSuccessCount, ag.ResetFailureCount = 2, 1, 1
	ag.ResetInputTokens, ag.ResetCacheReadTokens, ag.ResetTotalTokens = 60, 20, 80
	if err := db.Save(&ag).Error; err != nil {
		t.Fatal(err)
	}
	// Ineligible files must not affect metrics even when counters are large.
	for _, name := range []string{"disabled-metrics", "expired-metrics", "future-metrics", "deleted-metrics"} {
		id := summaryTestIdentity(t, db, name, "codex", "team")
		id.TotalRequests, id.TotalTokens = 999, 999999
		switch name {
		case "disabled-metrics":
			id.Disabled = ptr(true)
		case "expired-metrics":
			id.ActiveUntil = ptr(now)
		case "future-metrics":
			id.ActiveStart = ptr(now.Add(time.Hour))
		case "deleted-metrics":
			id.IsDeleted = true
		}
		if err := db.Save(&id).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Counters remain available with missing quota cache: they are independent of
	// 5h/Weekly snapshots, provider refreshes and current/estimated display mode.
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	wantCodex := SummaryUsageStats{TotalRequests: 98, SuccessCount: 52, FailureCount: 46, InputTokens: 980, CacheReadTokens: 160, TotalTokens: 1120}
	wantAG := SummaryUsageStats{TotalRequests: 4, SuccessCount: 3, FailureCount: 1, InputTokens: 140, CacheReadTokens: 30, TotalTokens: 320}
	if got.Eligible != 3 || len(got.Groups) != 3 {
		t.Fatalf("wrong metrics scope: eligible=%d groups=%d", got.Eligible, len(got.Groups))
	}
	if got.Groups[0].Stats != wantCodex {
		t.Fatalf("stats=%+v want=%+v", got.Groups[0].Stats, wantCodex)
	}
	if got.Groups[1].Stats != wantAG || got.Groups[2].Stats != wantAG {
		t.Fatal("Antigravity stats must reflect the same participating credential files")
	}
}

func TestSummaryMetricsRejectOverflow(t *testing.T) {
	stats := SummaryUsageStats{TotalTokens: math.MaxInt64}
	if err := stats.add(entities.UsageIdentity{TotalTokens: 1}); err == nil {
		t.Fatal("token counters wrapped around")
	}
}

func TestSummaryNextResetUsesEarliestValidWindowIndependently(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	fiveResets := []time.Duration{4 * time.Hour, 4 * time.Hour, 237 * time.Minute}
	weekResets := []time.Duration{151 * time.Hour, 147 * time.Hour, 147 * time.Hour}
	for i := range fiveResets {
		id := summaryTestIdentity(t, db, fmt.Sprintf("file-%d", i), "codex", "team")
		five := summaryTestRow(18000, 25, 100, 1, now)
		five.ResetAt = now.Add(fiveResets[i]).Format(time.RFC3339)
		week := summaryTestRow(604800, 50, 1000, 10, now)
		week.ResetAt = now.Add(weekResets[i]).Format(time.RFC3339)
		s.rememberSummarySnapshot(id.Identity, id.Type, withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{five, week}}), now, true)
	}
	// Invalid, reset and disabled windows cannot win the next reset time.
	for _, name := range []string{"expired", "invalid", "disabled"} {
		id := summaryTestIdentity(t, db, name, "codex", "team")
		five := summaryTestRow(18000, 25, 100, 1, now)
		five.ResetAt = now.Add(time.Minute).Format(time.RFC3339)
		switch name {
		case "expired":
			five.ResetAt = now.Add(-time.Minute).Format(time.RFC3339)
		case "invalid":
			five.ResetAt = "invalid"
		case "disabled":
			if err := db.Model(&id).Update("disabled", true).Error; err != nil {
				t.Fatal(err)
			}
		}
		s.rememberSummarySnapshot(id.Identity, id.Type, withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{five}}), now, true)
	}
	// Also verifies persisted snapshots after restart, rather than task timestamps.
	restarted := summaryTestService(db)
	got, err := restarted.quotaSummaryAt(context.Background(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for i, expected := range []time.Time{now.Add(237 * time.Minute), now.Add(147 * time.Hour)} {
		row := got.Groups[0].Quota[i]
		if row.Coverage.NextResetAt == nil || !row.Coverage.NextResetAt.Equal(expected) {
			t.Fatalf("window %d reset = %v; want %v", i, row.Coverage.NextResetAt, expected)
		}
		if row.ResetAt != "" {
			t.Fatal("individual next reset must not claim the aggregate resets together")
		}
	}
	// Once the earliest file has reset, the display advances to the next file.
	got, err = restarted.quotaSummaryAt(context.Background(), now.Add(237*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].Coverage.NextResetAt == nil || !got.Groups[0].Quota[0].Coverage.NextResetAt.Equal(now.Add(4*time.Hour)) {
		t.Fatal("next reset failed to advance past expired file")
	}
	got, err = restarted.quotaSummaryAt(context.Background(), now.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	row := got.Groups[0].Quota[0]
	if row.Coverage.NextResetAt == nil || !row.Coverage.NextResetAt.Equal(now.Add(-time.Minute)) || row.UsageState != "reset" {
		t.Fatal("reset window must keep its passed reset for the zero countdown")
	}
	assertNear(t, row.RemainingFraction, 1)
}

func TestSummaryCountsMixedPlansOncePerEligibleCredential(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	for i, plan := range []string{" TEAM ", "team", "plus", "pro", "prolite", "", "future-plan"} {
		id := summaryTestIdentity(t, db, fmt.Sprintf("codex-%d", i), "codex", plan)
		// Even expired or unavailable quotas still have a participating credential.
		if i == 0 {
			summaryTestCache(s, id, []QuotaRow{summaryTestRow(18000, 25, 100, 1, now.Add(-2*time.Hour))}, now.Add(-2*time.Hour))
		}
	}
	for _, name := range []string{"disabled", "deleted", "expired", "future"} {
		id := summaryTestIdentity(t, db, name, "codex", "team")
		updates := map[string]any{}
		switch name {
		case "disabled":
			updates["disabled"] = true
		case "deleted":
			updates["is_deleted"] = true
		case "expired":
			updates["active_until"] = now.Add(-time.Hour).Format(time.RFC3339Nano)
		case "future":
			updates["active_start"] = now.Add(time.Hour).Format(time.RFC3339Nano)
		}
		if err := db.Model(&id).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	want := []SummaryPlanCount{{"future-plan", 1}, {"plus", 1}, {"pro-20x", 1}, {"pro-5x", 1}, {"team", 2}, {"unknown", 1}}
	if len(got.Groups) != 1 || fmt.Sprint(got.Groups[0].Plans) != fmt.Sprint(want) || got.Eligible != 7 {
		t.Fatalf("unexpected plan counts: %+v", got)
	}
	// Successful response subscriptions override fallback identity metadata.
	id := summaryTestIdentity(t, db, "changed-plan", "codex", "team")
	summaryTestCache(s, id, []QuotaRow{summaryTestRow(18000, 25, 100, 1, now)}, now)
	s.refreshTasks[id.Identity].Quota.Subscription.Plan = "plus"
	got, err = s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Plans[1] != (SummaryPlanCount{"plus", 2}) {
		t.Fatal("current subscription was not used for display")
	}
}

func TestSummaryPlanCountsRestoreAntigravityTierAndStaySeparateFromCodex(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	summaryTestIdentity(t, db, "codex", "codex", "team")
	for i, tier := range []string{"g1-pro-tier", "g1-ultra-tier", "future-tier"} {
		id := summaryTestIdentity(t, db, fmt.Sprintf("ag-%d", i), "antigravity", "")
		for _, pool := range []string{antigravityGeminiGroupKey, antigravityClaudeGPTGroupKey} {
			row := summaryTestRow(18000, 25, 100, 1, now)
			row.Scope, row.GroupKey = "quota_group", pool
			response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{row}, Subscription: &SubscriptionInfo{Provider: "antigravity", Plan: "unknown", TierID: tier}})
			s.rememberSummarySnapshot(id.Identity, id.Type, response, now, false)
		}
	}
	// Counts restore from saved plan metadata without a provider request or tasks.
	got, err := summaryTestService(db).quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 3 || fmt.Sprint(got.Groups[0].Plans) != fmt.Sprint([]SummaryPlanCount{{"team", 1}}) {
		t.Fatalf("provider plans mixed: %+v", got)
	}
	want := []SummaryPlanCount{{"pro", 1}, {"ultra", 1}, {"unknown", 1}}
	for _, group := range got.Groups[1:] {
		if fmt.Sprint(group.Plans) != fmt.Sprint(want) {
			t.Fatalf("saved tiers not counted once: %+v", group.Plans)
		}
	}
}
