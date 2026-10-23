package migration

import (
	"cpa-usage-keeper/internal/entities"
	"gorm.io/gorm"
)

func createQuotaSummarySnapshotsMigration(tx *gorm.DB) error {
	return tx.AutoMigrate(&entities.QuotaSummarySnapshot{})
}
