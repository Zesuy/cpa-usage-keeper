package entities

import "time"

// QuotaCapacityEstimate retains only a credential's own latest valid estimate.
// Peer-filled values must never be written here.
type QuotaCapacityEstimate struct {
	AuthIndex     string `gorm:"primaryKey"`
	Provider      string `gorm:"primaryKey"`
	Pool          string `gorm:"primaryKey"`
	WindowSeconds int64  `gorm:"primaryKey"`
	PlanKey       string
	Tokens        float64
	CostUSD       float64
	ObservedAt    time.Time `gorm:"serializer:storageTime"`
}
