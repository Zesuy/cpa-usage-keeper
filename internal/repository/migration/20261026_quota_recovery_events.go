package migration

import (
	"cpa-usage-keeper/internal/entities"
	"gorm.io/gorm"
)

func createQuotaRecoveryEventsMigration(tx *gorm.DB) error {
	return tx.AutoMigrate(&entities.QuotaRecoveryEvent{})
}
