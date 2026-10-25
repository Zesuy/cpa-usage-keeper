package test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"gorm.io/gorm"
)

func botEvent(key, model string, at time.Time, cost float64, output, latency int64) entities.UsageEvent {
	return entities.UsageEvent{EventKey: fmt.Sprintf("%s-%s-%d-%d", key, model, at.UnixNano(), output), APIGroupKey: key, Model: model, Timestamp: at,
		InputTokens: 100, CacheReadTokens: 80, OutputTokens: output, TotalTokens: 100 + output, LatencyMS: latency, CostUSD: new(cost), CostAvailable: new(true)}
}

func TestBotUsageDisjointBoundariesTailAndActiveKeyScope(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 10, 25, 12, 30, 0, 0, time.Local)
	start := now.Add(-24 * time.Hour)
	keys := []entities.CPAAPIKey{{APIKey: "secret-bot", KeyAlias: "bot"}, {APIKey: "secret-other", KeyAlias: "other"}, {APIKey: "secret-deleted", KeyAlias: "deleted", IsDeleted: true}}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	events := []entities.UsageEvent{
		botEvent("secret-bot", "a", start.Add(-time.Nanosecond), 100, 100, 1000),
		botEvent("secret-bot", "a", start, 1, 100, 1000),
		botEvent("secret-bot", "a", start.Add(time.Hour), 2, 100, 10000),
		botEvent("secret-bot", "a", now.Add(-2*time.Hour), 3, 50, 1000),
		botEvent("secret-bot", "a", now.Add(-time.Minute), 0, 0, 0),
		botEvent("secret-bot", "a", now, 100, 100, 1000),
		botEvent("secret-other", "b", now.Add(-time.Hour), 7, 100, 1000),
		botEvent("secret-deleted", "deleted", now.Add(-time.Hour), 9, 100, 1000),
	}
	events[3].Failed = true
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	late := botEvent("secret-bot", "a", start.Add(2*time.Hour), 4, 100, 5000)
	if err := db.Create(&late).Error; err != nil {
		t.Fatal(err)
	}
	before, err := repository.LoadUsageAggregationCheckpointSnapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyName: "bot"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hours) != 25 || r.KeyID != keys[0].ID || r.KeyLabel != "bot" || r.Total.Requests != 5 || r.Total.Success != 4 || r.Total.Failure != 1 || r.Total.CostUSD != 10 || r.Total.SpeedSampleCount != 4 || r.Total.SpeedTPSSum != 180 {
		t.Fatalf("bad exact snapshot: %+v", r)
	}
	var requests, tokens, samples int64
	var cost float64
	for _, h := range r.Hours {
		requests += h.Requests
		tokens += h.TotalTokens
		samples += h.SpeedSampleCount
		cost += h.CostUSD
	}
	if requests != r.Total.Requests || tokens != r.Total.TotalTokens || samples != r.Total.SpeedSampleCount || cost != r.Total.CostUSD {
		t.Fatal("hour totals disagree")
	}
	if !r.Hours[0].Start.Equal(start) || !r.Hours[0].End.Equal(start.Truncate(time.Hour).Add(time.Hour)) || !r.Hours[24].End.Equal(now) {
		t.Fatal("partial boundary labels")
	}
	after, _ := repository.LoadUsageAggregationCheckpointSnapshot(context.Background(), db)
	if before != after {
		t.Fatal("read advanced aggregation")
	}
	byID, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyID: keys[0].ID}, now)
	if err != nil || byID.Total != r.Total {
		t.Fatal("ID/name scope differs", err)
	}
	global, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{}, now)
	if err != nil || global.Total.Requests != 6 || len(global.Models) != 2 {
		t.Fatal("global active key scope", err)
	}
	filtered, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{Models: []string{"b"}}, now)
	if err != nil || len(filtered.Models) != 1 || filtered.Total.Requests != 1 || filtered.Models[0].Model != "b" {
		t.Fatal("model filter", err)
	}
}

func TestBotUsageKeyFailuresEmptyAndMissingPrice(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	keys := []entities.CPAAPIKey{{APIKey: "private-a", KeyAlias: "bot"}, {APIKey: "private-b", KeyAlias: "bot"}, {APIKey: "private-c", KeyAlias: "empty"}, {APIKey: "private-deleted", KeyAlias: "gone", IsDeleted: true}}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bot", "missing", "gone", ""} {
		_, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyName: name}, now)
		var problem *repository.BotKeyError
		if !errors.As(err, &problem) {
			t.Fatalf("key %q did not fail: %v", name, err)
		}
		if strings.Contains(problem.Error(), "private-") {
			t.Fatal("key leaked")
		}
		if name == "bot" && len(problem.Candidates) != 2 {
			t.Fatal("missing duplicate candidates")
		}
	}
	r, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyID: keys[2].ID}, now)
	if err != nil || r.Total.Requests != 0 || len(r.Hours) != 24 || !r.Total.CostAvailable || len(r.Models) != 0 {
		t.Fatal("empty report", err)
	}
	e := botEvent("private-c", "free", now.Add(-time.Hour), 0, 100, 1000)
	e.CostAvailable = new(false)
	if err := db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	r, err = repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyID: keys[2].ID}, now)
	if err != nil || r.Total.CostAvailable || r.Total.SpeedSampleCount != 1 || r.Total.SpeedTPSSum != 100 {
		t.Fatal("unknown price must keep speed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.ReadBotUsage(ctx, db, repository.BotUsageQuery{}, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
}

func TestBotUsageFractionalUTCBoundariesAndStoredSpeed(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 500000000, time.UTC)
	start := now.Add(-24 * time.Hour)
	if err := db.Create(&entities.CPAAPIKey{APIKey: "utc", KeyAlias: "bot"}).Error; err != nil {
		t.Fatal(err)
	}
	events := []entities.UsageEvent{botEvent("utc", "m", start.Add(-time.Nanosecond), 99, 100, 1000), botEvent("utc", "m", start, 1, 100, 1000), botEvent("utc", "m", now.Add(-time.Millisecond), 2, 100, 5000), botEvent("utc", "m", now, 99, 100, 1000)}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	r, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{}, now)
	if err != nil || r.Total.Requests != 2 || r.Total.CostUSD != 3 || math.Abs(r.Total.SpeedTPSSum-120) > 1e-9 {
		t.Fatalf("fractional snapshot: %+v %v", r, err)
	}
}

func TestBotUsageReaderSnapshotSurvivesConcurrentCommit(t *testing.T) {
	db, _, _ := openTestDatabasePools(t, "bot-snapshot.db")
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	if err := db.Create(&entities.CPAAPIKey{APIKey: "snapshot", KeyAlias: "bot"}).Error; err != nil {
		t.Fatal(err)
	}
	e := botEvent("snapshot", "m", now.Add(-time.Hour), 1, 100, 1000)
	if err := db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var commitErr error
	name := "bot_snapshot_commit"
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "usage_aggregation_checkpoints" {
			once.Do(func() {
				newer := botEvent("snapshot", "m", now.Add(-2*time.Hour), 2, 100, 1000)
				commitErr = db.Create(&newer).Error
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
	r, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyName: "bot"}, now)
	if err != nil || commitErr != nil || r.Total.Requests != 1 {
		t.Fatalf("snapshot saw later commit: %+v %v %v", r, err, commitErr)
	}
	db.Callback().Query().Remove(name)
	r, err = repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{KeyScoped: true, KeyName: "bot"}, now)
	if err != nil || r.Total.Requests != 2 {
		t.Fatal("next snapshot missed commit", err)
	}
}

func TestBotUsageTimezoneHoursKeepDistinctInstants(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "Asia/Kathmandu"} {
		t.Run(zone, func(t *testing.T) {
			location, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			previous := time.Local
			time.Local = location
			t.Cleanup(func() { time.Local = previous })
			db := openTestDatabase(t)
			now := time.Date(2026, 10, 25, 12, 30, 0, 0, location)
			start := now.Add(-24 * time.Hour)
			if err := db.Create(&entities.CPAAPIKey{APIKey: "tz", KeyAlias: "bot"}).Error; err != nil {
				t.Fatal(err)
			}
			// One event in every elapsed hour; Berlin includes both repeated 02:xx hours.
			for i := 0; i < 24; i++ {
				e := botEvent("tz", "m", start.Add(time.Duration(i)*time.Hour), 1, 100, 1000)
				if err := db.Create(&e).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
				t.Fatal(err)
			}
			r, err := repository.ReadBotUsage(context.Background(), db, repository.BotUsageQuery{}, now)
			if err != nil || r.Total.Requests != 24 || r.Total.CostUSD != 24 || r.Total.SpeedSampleCount != 24 {
				t.Fatalf("timezone snapshot: %+v %v", r, err)
			}
			var requests int64
			for i, h := range r.Hours {
				requests += h.Requests
				if !h.End.After(h.Start) || i > 0 && !h.Start.Equal(r.Hours[i-1].End) {
					t.Fatal("hour intervals overlap or have a gap")
				}
			}
			if requests != 24 || !r.Hours[0].Start.Equal(start) || !r.Hours[len(r.Hours)-1].End.Equal(now) {
				t.Fatal("hour totals or bounds differ")
			}
		})
	}
}
