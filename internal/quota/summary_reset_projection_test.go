package quota

import (
	"context"
	"testing"
	"time"
)

func TestSummaryResetProjectionWeightsResetFilesAndResumesAfterRefresh(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	now := time.Now().Truncate(time.Second)
	a := summaryTestIdentity(t, db, "reset", "codex", "team")
	b := summaryTestIdentity(t, db, "active", "codex", "team")
	reset := summaryTestRow(18000, 50, 100, 1, now.Add(-time.Hour)) // capacity 200
	reset.ResetAt = now.Format(time.RFC3339)
	active := summaryTestRow(18000, 50, 200, 2, now) // capacity 400
	active.ResetAt = now.Add(time.Hour).Format(time.RFC3339)
	summaryTestCache(s, a, []QuotaRow{reset}, now.Add(-time.Hour))
	summaryTestCache(s, b, []QuotaRow{active}, now)
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	row := got.Groups[0].Quota[0]
	assertNear(t, row.WindowUsageEstimateTokens, 600)
	assertNear(t, row.RemainingFraction, 400.0/600)
	assertNear(t, row.WindowUsageCost, 2)
	if row.WindowUsageTokens == nil || *row.WindowUsageTokens != 200 || row.UsageState == "reset" || row.Coverage.Expired != 1 || row.Coverage.Historical != 1 || row.Coverage.NextResetAt == nil || !row.Coverage.NextResetAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("mixed reset projection incorrect: %+v", row)
	}
	// Reads must not rewrite task rows or stored snapshots with projected usage.
	if *s.refreshTasks[a.Identity].Quota.Quota[0].WindowUsageTokens != 100 || *s.refreshTasks[a.Identity].Quota.Quota[0].UsedPercent != 50 {
		t.Fatal("projection altered the last successful observation")
	}
	// Once all known files reset, retain real capacity but zero all old usage.
	got, err = s.quotaSummaryAt(context.Background(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	row = got.Groups[0].Quota[0]
	assertNear(t, row.RemainingFraction, 1)
	assertNear(t, row.WindowUsageCost, 0)
	assertNear(t, row.WindowUsageEstimateTokens, 600)
	if *row.WindowUsageTokens != 0 || row.UsageState != "reset" || row.EstimateState != "reset" || row.Coverage.NextResetAt.After(now.Add(time.Hour)) {
		t.Fatalf("all-reset projection incorrect: %+v", row)
	}
	// A real refreshed observation replaces the inferred full/zero state.
	fresh := summaryTestRow(18000, 25, 50, .5, now.Add(2*time.Hour))
	summaryTestCache(s, a, []QuotaRow{fresh}, now.Add(2*time.Hour))
	got, err = s.quotaSummaryAt(context.Background(), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].UsageState == "reset" || *got.Groups[0].Quota[0].WindowUsageTokens != 50 {
		t.Fatal("fresh observation did not replace reset projection")
	}
}

func TestSummaryResetWithoutCapacityDoesNotInventEstimates(t *testing.T) {
	for _, scenario := range []string{"reset", "missing", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			db := summaryTestDB(t)
			s := summaryTestService(db)
			now := time.Now().Truncate(time.Second)
			id := summaryTestIdentity(t, db, "a", "codex", "team")
			if scenario != "missing" {
				// A 100%-used row has no valid original numeric capacity estimate.
				row := summaryTestRow(18000, 100, 100, 1, now.Add(-time.Minute))
				row.ResetAt = now.Format(time.RFC3339)
				if scenario == "invalid" {
					row.ResetAt = "invalid"
				}
				summaryTestCache(s, id, []QuotaRow{row}, now.Add(-time.Minute))
			}
			got, err := s.quotaSummaryAt(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			row := got.Groups[0].Quota[0]
			if row.WindowUsageEstimateTokens != nil || row.WindowUsageEstimateCost != nil {
				t.Fatal("invented numeric capacity")
			}
			if scenario == "reset" {
				assertNear(t, row.RemainingFraction, 1)
				assertNear(t, row.WindowUsageCost, 0)
				if row.UsageState != "reset" || *row.WindowUsageTokens != 0 || row.Coverage.NextResetAt == nil || !row.Coverage.NextResetAt.Equal(now) {
					t.Fatal("known reset did not project full quota")
				}
			} else if row.RemainingFraction != nil || row.WindowUsageTokens != nil || row.UsageState == "reset" {
				t.Fatal("unavailable data was promoted to a full quota")
			}
		})
	}
}
