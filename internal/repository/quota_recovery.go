package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/plugin/dbresolver"
)

var ErrQuotaRecoveryCursor = errors.New("quota recovery cursor is ahead of database")

// InsertQuotaRecoveryEvent is idempotent even when a commit's acknowledgement
// was lost. The detector retains the same event key until persistence succeeds.
func InsertQuotaRecoveryEvent(ctx context.Context, db *gorm.DB, key string, event dto.QuotaRecoveryEvent) error {
	if key == "" || event.Type != "multi_account_weekly_recovery" || event.AccountCount != 3 || len(event.Accounts) != 3 {
		return fmt.Errorf("invalid quota recovery event")
	}
	event.ID, event.RecordedAt = "", time.Time{}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Clauses(dbresolver.Write, clause.OnConflict{Columns: []clause.Column{{Name: "event_key"}}, DoNothing: true}).Create(&entities.QuotaRecoveryEvent{
		EventKey: key, Payload: string(payload), CreatedAt: time.Now().UTC(),
	}).Error
}

func ReadQuotaRecoveryEvents(ctx context.Context, db *gorm.DB, afterID int64, limit int, startedAt time.Time) (dto.QuotaRecoveryEventsPage, error) {
	page := dto.QuotaRecoveryEventsPage{Events: []dto.QuotaRecoveryEvent{}, NextAfterID: strconv.FormatInt(afterID, 10), DetectorStartedAt: startedAt.UTC()}
	if afterID < 0 || limit < 1 || limit > 200 {
		return page, fmt.Errorf("invalid quota recovery pagination")
	}
	err := db.WithContext(ctx).Clauses(dbresolver.Read).Transaction(func(tx *gorm.DB) error {
		var maximum int64
		if err := tx.Model(&entities.QuotaRecoveryEvent{}).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
			return err
		}
		if afterID > maximum {
			return ErrQuotaRecoveryCursor
		}
		var rows []entities.QuotaRecoveryEvent
		if err := tx.Where("id > ? AND id <= ?", afterID, maximum).Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		page.HasMore = len(rows) > limit
		if page.HasMore {
			rows = rows[:limit]
		}
		for _, row := range rows {
			var event dto.QuotaRecoveryEvent
			if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
				return fmt.Errorf("decode quota recovery event: %w", err)
			}
			event.ID, event.RecordedAt = strconv.FormatInt(row.ID, 10), row.CreatedAt.UTC()
			page.Events = append(page.Events, event)
			page.NextAfterID = event.ID
		}
		return nil
	})
	return page, err
}
