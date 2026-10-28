package test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/repository/migration"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUpstreamServiceTierUpgradePreservesForkQuotaData(t *testing.T) {
	source := openUnmigratedTestDatabase(t)
	if err := source.AutoMigrate(entities.All()...); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)
	capacity := entities.QuotaCapacityEstimate{AuthIndex: "fixture", Provider: "codex", Pool: "codex", WindowSeconds: 604800, PlanKey: "team", Tokens: 10000, CostUSD: 281.11, ObservedAt: now}
	snapshot := entities.QuotaSummarySnapshot{AuthIndex: "fixture", Provider: "codex", Pool: "codex", WindowSeconds: 604800, PlanKey: "team", IdentityPlanKey: "team", Payload: `{"remainingFraction":0.63,"window":{"seconds":604800}}`, ObservedAt: now}
	for _, row := range []any{&capacity, &snapshot, &entities.UsageEvent{EventKey: "preserved-event", Timestamp: now, TotalTokens: 100, CostUSD: new(1.5), CostAvailable: new(true)}} {
		if err := source.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.InsertQuotaRecoveryEvent(context.Background(), source, "stable-recovery", dto.QuotaRecoveryEvent{Type: "multi_account_weekly_recovery", RuleVersion: 1, AccountCount: 3, Accounts: make([]dto.QuotaRecoveryEvidence, 3)}); err != nil {
		t.Fatal(err)
	}
	// Represent the last published fork schema, before the upstream field existed.
	for _, model := range []any{&entities.UsageEvent{}, &entities.UsageEventArchive{}} {
		if err := source.Migrator().DropColumn(model, "EffectiveServiceTier"); err != nil {
			t.Fatal(err)
		}
	}
	if err := migration.MarkAllAsApplied(source); err != nil {
		t.Fatal(err)
	}
	if err := source.Exec("DELETE FROM schema_migrations WHERE version = ?", "20261023_usage_event_effective_service_tier").Error; err != nil {
		t.Fatal(err)
	}
	oldPage, err := repository.ReadQuotaRecoveryEvents(context.Background(), source, 0, 50, now)
	if err != nil || len(oldPage.Events) != 1 {
		t.Fatalf("old cursor %+v %v", oldPage, err)
	}
	// Upgrade a completed local backup copy; the source is not migrated.
	var dbFile string
	if err := source.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&dbFile).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "upgrade-copy.db")
	if err := os.WriteFile(target, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(target), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	closeMigrationTestDatabase(t, db)
	if err := migration.Run(db); err != nil {
		t.Fatal(err)
	}
	if err := migration.Run(db); err != nil {
		t.Fatal(err)
	}
	var gotCapacity entities.QuotaCapacityEstimate
	var gotSnapshot entities.QuotaSummarySnapshot
	if err := db.First(&gotCapacity).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	if !capacity.ObservedAt.Equal(gotCapacity.ObservedAt) || !snapshot.ObservedAt.Equal(gotSnapshot.ObservedAt) {
		t.Fatal("observation instant changed")
	}
	// storageTime normalizes locations; compare values after matching the same instants.
	gotCapacity.ObservedAt = capacity.ObservedAt
	gotSnapshot.ObservedAt = snapshot.ObservedAt
	if !reflect.DeepEqual(capacity, gotCapacity) || !reflect.DeepEqual(snapshot, gotSnapshot) {
		t.Fatalf("fork quota records changed: %+v %+v", gotCapacity, gotSnapshot)
	}
	page, err := repository.ReadQuotaRecoveryEvents(context.Background(), db, 0, 50, now)
	if err != nil || !reflect.DeepEqual(page, oldPage) {
		t.Fatalf("events changed: %+v %v", page, err)
	}
	var cursor int64
	if err := db.Model(&entities.QuotaRecoveryEvent{}).Select("MAX(id)").Scan(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	next, err := repository.ReadQuotaRecoveryEvents(context.Background(), db, cursor, 50, now)
	if err != nil || len(next.Events) != 0 || next.NextAfterID != oldPage.NextAfterID {
		t.Fatalf("cursor changed: %+v %v", next, err)
	}
	var event entities.UsageEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.TotalTokens != 100 || event.CostUSD == nil || *event.CostUSD != 1.5 || event.EffectiveServiceTier != "" {
		t.Fatalf("usage changed: %+v", event)
	}
	for _, model := range []any{&entities.UsageEvent{}, &entities.UsageEventArchive{}} {
		if !db.Migrator().HasColumn(model, "EffectiveServiceTier") {
			t.Fatal("upstream field missing")
		}
	}
	original, err := gorm.Open(sqlite.Open("file:"+dbFile+"?mode=ro"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	closeMigrationTestDatabase(t, original)
	if original.Migrator().HasColumn(&entities.UsageEvent{}, "EffectiveServiceTier") {
		t.Fatal("source backup was migrated")
	}
}
