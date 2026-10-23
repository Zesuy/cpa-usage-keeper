package migration

import (
	"cpa-usage-keeper/internal/entities"
	"gorm.io/gorm"
)

func createQuotaCapacityEstimatesMigration(tx *gorm.DB) error {
	return tx.AutoMigrate(&entities.QuotaCapacityEstimate{})
}
