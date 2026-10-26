package test

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/migration"
)

func TestQuotaRecoveryMigrationAdditiveAndIdempotent(t *testing.T) {
	db := openUnmigratedTestDatabase(t)
	if err := db.AutoMigrate(&entities.UsageIdentity{}); err != nil {
		t.Fatal(err)
	}
	identity := entities.UsageIdentity{Identity: "preserved", AuthType: entities.UsageIdentityAuthTypeCredential}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	runOnlyMigration(t, db, "20261026_quota_recovery_events")
	event := entities.QuotaRecoveryEvent{EventKey: "stable", Payload: "{}", CreatedAt: time.Now().UTC()}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	runOnlyMigration(t, db, "20261026_quota_recovery_events")
	var rows, identities int64
	if err := db.Model(&entities.QuotaRecoveryEvent{}).Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("events %d %v", rows, err)
	}
	if err := db.Model(&entities.UsageIdentity{}).Where("identity = ?", "preserved").Count(&identities).Error; err != nil || identities != 1 {
		t.Fatalf("identity %d %v", identities, err)
	}
	if err := migration.Run(db); err != nil {
		t.Fatal(err)
	}
}
