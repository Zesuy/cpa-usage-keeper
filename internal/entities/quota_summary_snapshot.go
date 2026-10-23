package entities

import "time"

// QuotaSummarySnapshot stores only normalized summary windows, never credentials
// or raw upstream responses. One successful observation replaces each window.
type QuotaSummarySnapshot struct {
	AuthIndex       string `gorm:"primaryKey"`
	Provider        string `gorm:"primaryKey"`
	Pool            string `gorm:"primaryKey"`
	WindowSeconds   int64  `gorm:"primaryKey"`
	PlanKey         string
	IdentityPlanKey string
	Payload         string
	ObservedAt      time.Time `gorm:"serializer:storageTime"`
}
