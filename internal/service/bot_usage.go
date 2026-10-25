package service

import (
	"context"
	"time"

	"cpa-usage-keeper/internal/repository"
)

// BotUsageProvider is optional so existing UI providers and test doubles stay unchanged.
type BotUsageProvider interface {
	GetBotUsage(context.Context, repository.BotUsageQuery, time.Time) (*repository.BotUsageReport, error)
}

func (s *usageService) GetBotUsage(ctx context.Context, query repository.BotUsageQuery, now time.Time) (*repository.BotUsageReport, error) {
	return repository.ReadBotUsage(ctx, s.db, query, now)
}
