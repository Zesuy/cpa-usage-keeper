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

func TestUpstreamUpgradePreservesForkQuotaData(t *testing.T) {
	for _, version := range []string{"20261023_usage_event_effective_service_tier", "20261028_plugin_api_key_sources"} {
		t.Run(version, func(t *testing.T) { checkUpstreamUpgradePreservesForkQuotaData(t, version) })
	}
}

func checkUpstreamUpgradePreservesForkQuotaData(t *testing.T, version string) {
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
	// Represent the previously published fork, before the selected additive migration.
	fields := []struct {
		model any
		names []string
	}{
		{&entities.UsageEvent{}, []string{"EffectiveServiceTier"}},
		{&entities.UsageEventArchive{}, []string{"EffectiveServiceTier"}},
	}
	if version == "20261028_plugin_api_key_sources" {
		fields = []struct {
			model any
			names []string
		}{
			{&entities.UsageEvent{}, []string{"IsNativeKey", "AccessProvider"}},
			{&entities.UsageEventArchive{}, []string{"IsNativeKey", "AccessProvider"}},
			{&entities.CPAAPIKey{}, []string{"SourceType", "AccessProvider"}},
		}
	}
	if err := source.Create(&entities.CPAAPIKey{APIKey: "fixture-key", KeyAlias: "bot"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range fields {
		for _, name := range table.names {
			if err := source.Migrator().DropColumn(table.model, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := migration.MarkAllAsApplied(source); err != nil {
		t.Fatal(err)
	}
	if err := source.Exec("DELETE FROM schema_migrations WHERE version = ?", version).Error; err != nil {
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
	for _, table := range fields {
		for _, name := range table.names {
			if !db.Migrator().HasColumn(table.model, name) {
				t.Fatalf("upstream field %s missing", name)
			}
		}
	}
	var key entities.CPAAPIKey
	if err := db.First(&key).Error; err != nil {
		t.Fatal(err)
	}
	if key.KeyAlias != "bot" || key.APIKey != "fixture-key" || key.SourceType != entities.CPAAPIKeySourceNative || key.AccessProvider != "" {
		t.Fatalf("existing native key changed: %+v", key)
	}
	if version == "20261028_plugin_api_key_sources" && (event.IsNativeKey != nil || event.AccessProvider != nil) {
		t.Fatal("historical request source was backfilled")
	}
	original, err := gorm.Open(sqlite.Open("file:"+dbFile+"?mode=ro"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	closeMigrationTestDatabase(t, original)
	for _, table := range fields {
		for _, name := range table.names {
			if original.Migrator().HasColumn(table.model, name) {
				t.Fatal("source backup was migrated")
			}
		}
	}
}
