package entities

import "time"

// QuotaRecoveryEvent stores only confirmed three-account incidents. Baselines,
// reset barriers and individual hits deliberately never enter this table.
type QuotaRecoveryEvent struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	EventKey  string    `gorm:"not null;uniqueIndex:uniq_quota_recovery_event_key"`
	Payload   string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"serializer:storageTime;not null"`
}
