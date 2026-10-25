package repository

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// BotUsageQuery distinguishes a required Key scope from the explicitly global TPS query.
type BotUsageQuery struct {
	KeyScoped bool
	KeyName   string
	KeyID     int64
	Models    []string
}

type BotKeyCandidate struct {
	ID               int64
	Label, MaskedKey string
}

type BotKeyError struct {
	Candidates []BotKeyCandidate
}

func (e *BotKeyError) Error() string {
	if len(e.Candidates) == 0 {
		return "未找到指定的有效 Key，不会切换为全站查询"
	}
	return "Key 名称重复，请使用 key_id 明确选择"
}

// BotUsageMetrics retains sums/counts until final rendering; means must not be averaged.
type BotUsageMetrics struct {
	Requests, Success, Failure                        int64
	InputTokens, OutputTokens, ReasoningTokens        int64
	CacheReadTokens, CacheCreationTokens, TotalTokens int64
	CostUSD                                           float64
	CostAvailable                                     bool
	SpeedTPSSum                                       float64
	SpeedSampleCount                                  int64
}

func (m *BotUsageMetrics) add(n BotUsageMetrics) {
	m.Requests += n.Requests
	m.Success += n.Success
	m.Failure += n.Failure
	m.InputTokens += n.InputTokens
	m.OutputTokens += n.OutputTokens
	m.ReasoningTokens += n.ReasoningTokens
	m.CacheReadTokens += n.CacheReadTokens
	m.CacheCreationTokens += n.CacheCreationTokens
	m.TotalTokens += n.TotalTokens
	m.CostUSD += n.CostUSD
	m.CostAvailable = m.CostAvailable && n.CostAvailable
	m.SpeedTPSSum += n.SpeedTPSSum
	m.SpeedSampleCount += n.SpeedSampleCount
}

type BotUsageHour struct {
	Start, End time.Time
	BotUsageMetrics
}

type BotModelUsage struct {
	Model string
	BotUsageMetrics
	Hours []BotUsageMetrics
}

type BotUsageReport struct {
	Start, End time.Time
	KeyID      int64
	KeyLabel   string
	Total      BotUsageMetrics
	Hours      []BotUsageHour
	Models     []BotModelUsage
}

type botUsageProjection struct {
	BucketStart     time.Time
	Model           string
	BotUsageMetrics `gorm:"embedded"`
	UnavailableRows int64
}

// ReadBotUsage uses one reader transaction for Key resolution, rollups, cursor and
// events. Full hours at/below the cursor, unaggregated full-hour events, and the
// two partial boundary intervals form disjoint sets. It never advances a cursor.
func ReadBotUsage(ctx context.Context, db *gorm.DB, query BotUsageQuery, now time.Time) (*BotUsageReport, error) {
	if db == nil {
		return nil, fmt.Errorf("bot usage database is not configured")
	}
	now = timeutil.NormalizeStorageTime(now)
	report := &BotUsageReport{Start: now.Add(-24 * time.Hour), End: now, Total: BotUsageMetrics{CostAvailable: true}}
	for at := report.Start; at.Before(now); {
		next := at.Truncate(time.Hour).Add(time.Hour)
		if next.After(now) {
			next = now
		}
		report.Hours = append(report.Hours, BotUsageHour{Start: at, End: next, BotUsageMetrics: BotUsageMetrics{CostAvailable: true}})
		at = next
	}
	models := map[string]*BotModelUsage{}
	add := func(model string, stamp time.Time, metrics BotUsageMetrics) error {
		if !finiteBotNumber(metrics.CostUSD) || !finiteBotNumber(metrics.SpeedTPSSum) {
			return fmt.Errorf("invalid stored bot metrics")
		}
		index := sort.Search(len(report.Hours), func(i int) bool { return report.Hours[i].End.After(stamp) })
		if index == len(report.Hours) || stamp.Before(report.Hours[index].Start) {
			return nil
		}
		model = normalizeUsageOverviewDimension(model)
		entry := models[model]
		if entry == nil {
			entry = &BotModelUsage{Model: model, BotUsageMetrics: BotUsageMetrics{CostAvailable: true}, Hours: make([]BotUsageMetrics, len(report.Hours))}
			for i := range entry.Hours {
				entry.Hours[i].CostAvailable = true
			}
			models[model] = entry
		}
		entry.add(metrics)
		entry.Hours[index].add(metrics)
		report.Total.add(metrics)
		report.Hours[index].add(metrics)
		return nil
	}
	err := db.WithContext(ctx).Clauses(dbresolver.Read).Transaction(func(tx *gorm.DB) error {
		apiKey := ""
		if query.KeyScoped {
			var keys []entities.CPAAPIKey
			lookup := tx.Where("is_deleted = ?", false)
			if query.KeyID > 0 {
				lookup = lookup.Where("id = ?", query.KeyID)
			} else {
				if strings.TrimSpace(query.KeyName) == "" {
					return &BotKeyError{}
				}
				lookup = lookup.Where("key_alias = ?", strings.TrimSpace(query.KeyName))
			}
			if err := lookup.Order("id asc").Find(&keys).Error; err != nil {
				return err
			}
			if len(keys) != 1 {
				problem := &BotKeyError{}
				for _, key := range keys {
					problem.Candidates = append(problem.Candidates, BotKeyCandidate{key.ID, helper.CPAAPIKeyDisplayName(key), helper.CPAAPIKeyMaskedDisplayKey(key)})
				}
				return problem
			}
			apiKey = keys[0].APIKey
			report.KeyID, report.KeyLabel = keys[0].ID, helper.CPAAPIKeyDisplayName(keys[0])
		}
		cursor, err := LoadUsageAggregationCheckpointSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		fullStart := report.Start.Truncate(time.Hour)
		if fullStart.Before(report.Start) {
			fullStart = fullStart.Add(time.Hour)
		}
		fullEnd := now.Truncate(time.Hour)
		filter := func(q *gorm.DB, table string) *gorm.DB {
			q = q.Joins("INNER JOIN cpa_api_keys ON cpa_api_keys.api_key = "+table+".api_group_key AND cpa_api_keys.is_deleted = ?", false)
			if query.KeyScoped {
				q = q.Where(table+".api_group_key = ?", apiKey)
			}
			if len(query.Models) > 0 {
				q = q.Where(table+".model IN ?", query.Models)
			}
			return q
		}
		if fullEnd.After(fullStart) {
			bucketRead := overviewBucketRead(fullStart, fullEnd, "hourly")
			q := bucketRead.query(filter(tx.Model(&entities.UsageOverviewHourlyStat{}), "usage_overview_hourly_stats"))
			var rows []botUsageProjection
			if err := q.Select(`bucket_start, model, SUM(request_count) AS requests, SUM(success_count) AS success, SUM(failure_count) AS failure,
			 SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens, SUM(reasoning_tokens) AS reasoning_tokens,
			 SUM(cache_read_tokens) AS cache_read_tokens, SUM(cache_creation_tokens) AS cache_creation_tokens, SUM(total_tokens) AS total_tokens,
			 SUM(COALESCE(cost_usd, 0)) AS cost_usd, SUM(speed_tps_sum) AS speed_tps_sum, SUM(speed_sample_count) AS speed_sample_count,
			 SUM(CASE WHEN cost_usd IS NULL OR unavailable_cost_count IS NULL OR unavailable_cost_count > 0 THEN 1 ELSE 0 END) AS unavailable_rows`).
				Group("bucket_start, model").Order("bucket_start, model").Scan(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				row.CostAvailable = row.UnavailableRows == 0
				if err := add(row.Model, bucketRead.displayStart(row.BucketStart), row.BotUsageMetrics); err != nil {
					return err
				}
			}
		}
		// Explicit, disjoint intervals avoid comparing RFC3339 text at fractional
		// second or offset boundaries outside the shared time reader.
		windows := []struct {
			start, end time.Time
			tail       bool
		}{
			{report.Start, fullStart, false}, {fullStart, fullEnd, true}, {fullEnd, now, false},
		}
		for _, window := range windows {
			for _, read := range usageEventTimeReads(window.start, window.end, false) {
				q := read.query(filter(tx.Model(&entities.UsageEvent{}), "usage_events"))
				var events []entities.UsageEvent
				if window.tail {
					q = q.Where("usage_events.id > ?", cursor.OverviewCursor)
				}
				if err := q.Select("usage_events.id, timestamp, model, failed, input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_creation_tokens, total_tokens, latency_ms, cost_usd, cost_available").Find(&events).Error; err != nil {
					return err
				}
				for _, event := range events {
					if !read.contains(event.Timestamp) {
						continue
					}
					m := BotUsageMetrics{Requests: 1, InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, ReasoningTokens: event.ReasoningTokens,
						CacheReadTokens: event.CacheReadTokens, CacheCreationTokens: event.CacheCreationTokens, TotalTokens: event.TotalTokens,
						CostAvailable: event.CostUSD != nil && event.CostAvailable != nil && *event.CostAvailable}
					if event.Failed {
						m.Failure = 1
					} else {
						m.Success = 1
					}
					if event.CostUSD != nil {
						m.CostUSD = *event.CostUSD
					}
					if event.OutputTokens > 0 && event.LatencyMS > 0 {
						m.SpeedTPSSum = float64(event.OutputTokens) * 1000 / float64(event.LatencyMS)
						m.SpeedSampleCount = 1
					}
					if err := add(event.Model, event.Timestamp, m); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		report.Models = append(report.Models, *model)
	}
	sort.Slice(report.Models, func(i, j int) bool { return report.Models[i].Model < report.Models[j].Model })
	return report, nil
}

func finiteBotNumber(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
