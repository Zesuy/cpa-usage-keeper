package quota

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
)

func TestSummaryArgonAttributionUpgradeIsolatesLegacyGemini(t *testing.T) {
	db := summaryTestDB(t)
	now := time.Now().Truncate(time.Second)
	id := summaryTestIdentity(t, db, "legacy-ag", "antigravity", "pro")
	rowFor := func(pool string) QuotaRow {
		row := summaryTestRow(18000, 50, 100, 1, now)
		row.Scope, row.GroupKey, row.GroupLabel, row.Key = "quota_group", pool, pool, "bucket."+pool+".5h"
		return withWindowUsageEstimates(CheckResponse{Quota: []QuotaRow{row}}).Quota[0]
	}
	for _, pool := range []string{antigravityGeminiGroupKey, antigravityClaudeGPTGroupKey} {
		row := rowFor(pool)
		payload, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&entities.QuotaSummarySnapshot{AuthIndex: id.Identity, Provider: "antigravity", Pool: pool, WindowSeconds: 18000, PlanKey: "plan:pro", Payload: string(payload), ObservedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&entities.QuotaCapacityEstimate{AuthIndex: id.Identity, Provider: "antigravity", Pool: pool, WindowSeconds: 18000, PlanKey: "plan:pro", Tokens: 99999, CostUSD: 999, ObservedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := summaryTestService(db)
	got, err := s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Pool != antigravityGeminiGroupKey || got.Groups[0].Quota[0].WindowUsageTokens != nil || got.Groups[0].Quota[0].WindowUsageEstimateTokens != nil {
		t.Fatalf("legacy Gemini snapshot reused: %+v", got.Groups[0])
	}
	if got.Groups[1].Quota[0].WindowUsageTokens == nil {
		t.Fatal("unaffected Claude/GPT snapshot lost")
	}
	// An empty current Gemini observation must not borrow the old mixed capacity,
	// either directly or as a peer sample for another credential with the same plan.
	peer := summaryTestIdentity(t, db, "new-ag", "antigravity", "pro")
	empty := rowFor(antigravityGeminiGroupKey)
	empty.WindowUsageTokens, empty.WindowUsageCost = ptr(int64(0)), ptr(0.0)
	empty.WindowUsageEstimateTokens, empty.WindowUsageEstimateCost = nil, nil
	summaryTestCache(s, id, []QuotaRow{empty}, now)
	summaryTestCache(s, peer, []QuotaRow{empty}, now)
	got, err = s.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Quota[0].WindowUsageEstimateTokens != nil {
		t.Fatal("legacy capacity reused as history or peer")
	}
	// Corrected observations persist under the new internal namespace and survive restart.
	row := rowFor(antigravityGeminiGroupKey)
	response := CheckResponse{Quota: []QuotaRow{row}, Subscription: &SubscriptionInfo{Provider: "antigravity", Plan: "pro"}}
	s.rememberCapacityEstimates(id.Identity, id.Type, response, now)
	s.rememberSummarySnapshot(id.Identity, id.Type, response, now, true)
	restarted := summaryTestService(db)
	got, err = restarted.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 2 || got.Groups[0].Pool != antigravityGeminiGroupKey {
		t.Fatalf("public pools changed: %+v", got)
	}
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateTokens, 200)
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateCost, 2)
	// A later zero-progress refresh can use corrected own history after restart.
	summaryTestCache(restarted, id, []QuotaRow{empty}, now)
	got, err = restarted.quotaSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, got.Groups[0].Quota[0].WindowUsageEstimateTokens, 200)
	if got.Groups[0].Quota[0].Coverage.Historical != 1 {
		t.Fatal("corrected historical capacity unavailable")
	}
}
