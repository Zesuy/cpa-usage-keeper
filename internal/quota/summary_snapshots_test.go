package quota

import (
	"context"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
)

func TestSummarySnapshotsSurviveRefreshFailureRestartAndHourlyCadence(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "snapshot-file", "codex", "team")
	now := time.Now().Truncate(time.Second)
	five := summaryTestRow(18000, 25, 100, 1, now)
	five.ResetAt = now.Add(2 * time.Hour).Format(time.RFC3339)
	week := summaryTestRow(604800, 50, 1000, 10, now)
	week.ResetAt = now.Add(6 * 24 * time.Hour).Format(time.RFC3339)
	task := &RefreshTaskRecord{AuthIndex: id.Identity, Type: id.Type, Status: RefreshTaskStatusRunning}
	s.refreshTasks[id.Identity] = task
	s.markRefreshTaskCompleted(task, CheckResponse{ID: "private-id", Quota: []QuotaRow{five, week}, Subscription: &SubscriptionInfo{Provider: "codex", Plan: "team"}}, nil)
	at := task.RefreshedAt
	// The 1-hour schedule must not leave 45 minutes without quota data.
	result, err := s.quotaSummaryAt(context.Background(), at.Add(45*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	row := result.Groups[0].Quota[0]
	assertNear(t, row.RemainingFraction, .75)
	if row.Coverage.Current != 1 || row.Coverage.Stale != 1 || row.UsageState != "cached" || row.Coverage.OldestSnapshotAt == nil || !row.Coverage.OldestSnapshotAt.Equal(at) {
		t.Fatalf("45-minute snapshot lost: %+v", row)
	}
	// Queue replacement must not discard the last successful values.
	s.refreshTasks[id.Identity] = &RefreshTaskRecord{AuthIndex: id.Identity, Type: id.Type, Status: RefreshTaskStatusRunning}
	result, err = s.quotaSummaryAt(context.Background(), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	row = result.Groups[0].Quota[0]
	if result.Pending != 1 || row.Coverage.Pending != 1 || row.Coverage.Retained != 1 || row.UsageState != "cached" {
		t.Fatalf("refresh cleared snapshot: %+v", row)
	}
	assertNear(t, row.RemainingFraction, .75)
	s.refreshTasks[id.Identity].Status = RefreshTaskStatusFailed
	s.refreshTasks[id.Identity].Quota = nil
	result, err = s.quotaSummaryAt(context.Background(), at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.Groups[0].Quota[0].Coverage.Failed != 1 {
		t.Fatal("failure state lost")
	}
	assertNear(t, result.Groups[0].Quota[0].WindowUsageCost, 1)
	// New process, no refresh task or frontend state: persisted normalized windows suffice.
	restarted := summaryTestService(db)
	result, err = restarted.quotaSummaryAt(context.Background(), at.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, result.Groups[0].Quota[0].WindowUsageEstimateTokens, 400)
	if result.Groups[0].Quota[0].Coverage.Retained != 1 {
		t.Fatal("restart did not restore saved window")
	}
	// Reset expires only the 5h data; Weekly remains valid and clearly marked old.
	result, err = restarted.quotaSummaryAt(context.Background(), now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	row = result.Groups[0].Quota[0]
	if row.UsageState != "reset" || row.EstimateState != "reset" || row.RemainingFraction != nil || row.WindowUsageTokens != nil || row.Coverage.Expired != 1 {
		t.Fatalf("old 5h data crossed reset: %+v", row)
	}
	assertNear(t, result.Groups[0].Quota[1].RemainingFraction, .5)
	// Neither reads nor peer fills write any new observations.
	var stored []entities.QuotaSummarySnapshot
	if err := db.Find(&stored).Error; err != nil || len(stored) != 2 {
		t.Fatalf("unexpected snapshots: %d %v", len(stored), err)
	}
	for _, value := range stored {
		if strings.Contains(value.Payload, "private-id") || strings.Contains(value.Payload, "subscription") {
			t.Fatal("snapshot stored non-window metadata")
		}
	}
	restarted.InvalidateStoredCostCache()
	result, err = restarted.quotaSummaryAt(context.Background(), at.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.Groups[0].Quota[0].UsageState != "missing" {
		t.Fatal("cost invalidation restored obsolete snapshot")
	}
	var count int64
	db.Model(&entities.QuotaSummarySnapshot{}).Count(&count)
	if count != 0 {
		t.Fatal("cost invalidation retained saved snapshots")
	}
}

func TestSummarySnapshotPartialHeaderKeepsIndependentWindowAndPlan(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "header-file", "codex", "team")
	now := time.Now().Truncate(time.Second)
	response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 25, 100, 1, now), summaryTestRow(604800, 50, 1000, 10, now)}})
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	later := now.Add(5 * time.Minute)
	// Partial headers are saved by pool/window and must not rejuvenate untouched rows.
	header := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 50, 400, 4, later)}})
	s.rememberSummarySnapshot(id.Identity, id.Type, header, later, false)
	restarted := summaryTestService(db)
	got, err := restarted.quotaSummaryAt(context.Background(), later.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateTokens, 800)
	if !got.Groups[0].Quota[0].Coverage.OldestSnapshotAt.Equal(later) || !got.Groups[0].Quota[1].Coverage.OldestSnapshotAt.Equal(now) {
		t.Fatal("partial header changed untouched observation time")
	}
	// A relative reset is anchored to the persisted observation, never to each read.
	relative := summaryTestRow(18000, 25, 100, 1, later)
	relative.ResetAt = ""
	relative.ResetAfterSeconds = ptr(int64(10))
	s.rememberSummarySnapshot(id.Identity, id.Type, withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{relative}}), later, false)
	got, err = summaryTestService(db).quotaSummaryAt(context.Background(), later.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].UsageState != "reset" {
		t.Fatal("relative reset slid forward after restart")
	}
	// Identity plan changes invalidate restored snapshots even before the next refresh.
	if err := db.Model(&id).Update("plan_type", "plus").Error; err != nil {
		t.Fatal(err)
	}
	got, err = summaryTestService(db).quotaSummaryAt(context.Background(), later.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].WindowUsageTokens != nil {
		t.Fatal("old plan restored")
	}
}

func TestSummaryMissingReasonsDoNotInventData(t *testing.T) {
	for _, test := range []struct {
		name   string
		status RefreshTaskStatus
		want   string
	}{
		{"missing", "", "missing"}, {"pending", RefreshTaskStatusQueued, "pending"}, {"failed", RefreshTaskStatusFailed, "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := summaryTestDB(t)
			s := summaryTestService(db)
			id := summaryTestIdentity(t, db, "a", "codex", "team")
			if test.status != "" {
				s.refreshTasks[id.Identity] = &RefreshTaskRecord{Status: test.status}
			}
			got, err := s.GetQuotaSummary(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			row := got.Groups[0].Quota[0]
			if row.UsageState != test.want || row.WindowUsageTokens != nil || row.RemainingFraction != nil {
				t.Fatalf("%+v", row)
			}
		})
	}
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "zero", "codex", "team")
	now := time.Now()
	summaryTestCache(s, id, []QuotaRow{summaryTestRow(18000, 0, 0, 0, now)}, now)
	got, err := s.GetQuotaSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := got.Groups[0].Quota[0]
	if row.WindowUsageTokens == nil || *row.WindowUsageTokens != 0 || row.UsageState != "current" || row.EstimateState != "insufficient_samples" {
		t.Fatal("valid zero confused with missing capacity")
	}
}

func TestSummarySnapshotWriteFailurePreservesSessionAndInvalidationFailsClosed(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "a", "codex", "team")
	now := time.Now()
	if err := db.Exec("CREATE TRIGGER reject_snapshot BEFORE INSERT ON quota_summary_snapshots BEGIN SELECT RAISE(ABORT, 'test write blocked'); END").Error; err != nil {
		t.Fatal(err)
	}
	response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 25, 100, 1, now)}})
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	got, err := s.quotaSummaryAt(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateTokens, 400)
	if err := db.Exec("DROP TRIGGER reject_snapshot").Error; err != nil {
		t.Fatal(err)
	}
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	if err := db.Exec("CREATE TRIGGER reject_snapshot_delete BEFORE DELETE ON quota_summary_snapshots BEGIN SELECT RAISE(ABORT, 'test delete blocked'); END").Error; err != nil {
		t.Fatal(err)
	}
	s.InvalidateStoredCostCache()
	got, err = s.quotaSummaryAt(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].WindowUsageCost != nil {
		t.Fatal("failed invalidation reused obsolete saved cost")
	}
	if err := db.Exec("DROP TRIGGER reject_snapshot_delete").Error; err != nil {
		t.Fatal(err)
	}
	s.rememberCapacityEstimates(id.Identity, id.Type, response, now)
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	if s.capacityHistoryInvalid {
		t.Fatal("invalidation retry did not recover")
	}
}

func TestSummarySnapshotFailedReplacementDoesNotRestoreRemovedWindow(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "a", "codex", "team")
	now := time.Now()
	response := withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 25, 100, 1, now), summaryTestRow(604800, 50, 1000, 10, now)}})
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	if err := db.Exec("CREATE TRIGGER reject_snapshot BEFORE INSERT ON quota_summary_snapshots BEGIN SELECT RAISE(ABORT, 'test write blocked'); END").Error; err != nil {
		t.Fatal(err)
	}
	// The transaction rolls back its deletes, but this process must still honor
	// the successful provider response rather than resurrect an omitted window.
	response.Quota = response.Quota[:1]
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	got, err := s.quotaSummaryAt(context.Background(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].WindowUsageTokens == nil || *got.Groups[0].Quota[0].WindowUsageTokens != 100 {
		t.Fatal("session snapshot lost after failed write")
	}
	if got.Groups[0].Quota[1].WindowUsageTokens != nil {
		t.Fatal("rolled-back disk snapshot resurrected a removed window")
	}
}

func TestSummarySnapshotPartialDuplicateCannotRestoreAmbiguousWindow(t *testing.T) {
	db := summaryTestDB(t)
	s := summaryTestService(db)
	id := summaryTestIdentity(t, db, "a", "codex", "team")
	now := time.Now()
	five := summaryTestRow(18000, 25, 100, 1, now)
	week := summaryTestRow(604800, 50, 1000, 10, now)
	s.rememberSummarySnapshot(id.Identity, id.Type, withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{five, week}}), now, true)
	s.rememberSummarySnapshot(id.Identity, id.Type, withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{five, five}}), now, false)
	for _, service := range []*Service{s, summaryTestService(db)} {
		got, err := service.quotaSummaryAt(context.Background(), now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if got.Groups[0].Quota[0].WindowUsageTokens != nil {
			t.Fatal("ambiguous partial publication reused a previous window")
		}
		if got.Groups[0].Quota[1].WindowUsageTokens == nil || *got.Groups[0].Quota[1].WindowUsageTokens != 1000 {
			t.Fatal("partial publication lost untouched weekly window")
		}
	}
}

func TestSummaryPartialHeaderAfterRestartRetainsUnchangedWeeklySnapshot(t *testing.T) {
	db := summaryTestDB(t)
	original := summaryTestService(db)
	id := summaryTestIdentity(t, db, "a", "codex", "team")
	now := time.Now()
	original.rememberSummarySnapshot(id.Identity, id.Type, withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 25, 100, 1, now), summaryTestRow(604800, 50, 1000, 10, now)}}), now, true)
	restarted := summaryTestService(db)
	later := now.Add(5 * time.Minute)
	header := CheckResponse{Quota: []QuotaRow{summaryTestRow(18000, 50, 400, 4, later)}}
	if !restarted.mergeUsageHeaderQuotaCache(id.Identity, header, later, id, 0) {
		t.Fatal("partial header was not published")
	}
	got, err := restarted.quotaSummaryAt(context.Background(), later.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateTokens, 800)
	week := got.Groups[0].Quota[1]
	assertNear(t, week.WindowUsageEstimateTokens, 2000)
	if week.UsageState != "cached" || week.Coverage.Retained != 1 || !week.Coverage.OldestSnapshotAt.Equal(now) {
		t.Fatalf("partial header discarded or rejuvenated Weekly: %+v", week)
	}
}
