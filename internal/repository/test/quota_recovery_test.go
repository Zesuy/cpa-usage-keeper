package test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
)

func TestQuotaRecoveryPersistenceAndPagination(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	started := time.Now().UTC()
	event := dto.QuotaRecoveryEvent{Type: "multi_account_weekly_recovery", RuleVersion: 1, AccountCount: 3, Accounts: make([]dto.QuotaRecoveryEvidence, 3), DetectedAt: started, KeeperResetExcluded: true, ExternalResetStatus: "unknown"}
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("stable-event-%d", i)
		if err := repository.InsertQuotaRecoveryEvent(ctx, db, key, event); err != nil {
			t.Fatal(err)
		}
		if err := repository.InsertQuotaRecoveryEvent(ctx, db, key, event); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.ReadQuotaRecoveryEvents(ctx, db, 0, 2, started)
	if err != nil || len(page.Events) != 2 || !page.HasMore || page.NextAfterID != page.Events[1].ID || !page.DetectorStartedAt.Equal(started) {
		t.Fatalf("first page %+v: %v", page, err)
	}
	if page.Events[0].ID != "1" || page.Events[0].RecordedAt.IsZero() || !page.Events[0].DetectedAt.Equal(started) {
		t.Fatalf("persisted event %+v", page.Events[0])
	}
	// Duplicate inserts may leave autoincrement gaps; clients must use returned cursors.
	var after int64
	if _, err := fmt.Sscan(page.NextAfterID, &after); err != nil {
		t.Fatal(err)
	}
	page, err = repository.ReadQuotaRecoveryEvents(ctx, db, after, 2, started.Add(time.Hour))
	if err != nil || len(page.Events) != 1 || page.HasMore || !page.DetectorStartedAt.Equal(started.Add(time.Hour)) {
		t.Fatalf("restart page %+v: %v", page, err)
	}
	if _, err := fmt.Sscan(page.NextAfterID, &after); err != nil {
		t.Fatal(err)
	}
	page, err = repository.ReadQuotaRecoveryEvents(ctx, db, after, 50, started)
	if err != nil || len(page.Events) != 0 || page.HasMore || page.NextAfterID != fmt.Sprint(after) {
		t.Fatalf("empty page %+v: %v", page, err)
	}
	if _, err := repository.ReadQuotaRecoveryEvents(ctx, db, after+100, 50, started); !errors.Is(err, repository.ErrQuotaRecoveryCursor) {
		t.Fatal("cursor ahead", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.ReadQuotaRecoveryEvents(cancelled, db, 0, 50, started); err == nil {
		t.Fatal("ignored cancellation")
	}
	var count int64
	if err := db.Model(&entities.QuotaRecoveryEvent{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("idempotence %d: %v", count, err)
	}
}
