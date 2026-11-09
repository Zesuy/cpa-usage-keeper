package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/service"
)

func TestUsageEventsLiveCostRevisionReplacesOldSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		observeBusy, wakeAfter bool
	}{
		{"observed_busy", true, false},
		{"short_write_with_wake", false, true},
		{"short_write_without_wake", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAPITestDatabase(t)
			now := time.Now()
			seed := liveTestInsert(t, db, []entities.UsageEvent{{Timestamp: now.Add(-time.Minute), Model: "match"}})
			notifier := service.NewUsageEventsNotifier()
			gate := service.NewCostReadGate()
			provider := service.NewUsageService(db, emptyPricingCatalogForTest())
			server := httptest.NewServer(api.NewRouter(nil, nil, provider, nil, api.AuthConfig{}, nil, "", api.OptionalProviders{UsageEventsLive: notifier, CostReadGate: gate}))
			defer server.Close()
			client := &http.Client{Timeout: 20 * time.Second}
			target := server.URL + "/api/v1/usage/events/live?range=24h"
			resp, reader := liveTestConnect(t, client, target, nil)
			defer resp.Body.Close()
			snapshot := liveTestReadKind(t, reader, "snapshot")
			if row := snapshot.data["events"].([]any)[0].(map[string]any); row["cost_usd"] != 1.25 {
				t.Fatalf("unexpected original cost: %+v", row)
			}
			// Wait for initial catch-up before blocking. Without a wake during the
			// short write phase, the stream cannot observe busy; revision must detect it.
			liveTestReadKind(t, reader, "checkpoint")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			resume, err := gate.BlockAndDrain(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer resume()
			late := liveTestInsert(t, db, []entities.UsageEvent{{Timestamp: now.Add(-30 * time.Second), Model: "match"}})
			if tc.observeBusy {
				notifier.NotifyUsageEventsCommitted(late)
				liveTestReadKind(t, reader, "busy")
			}
			if err := db.Model(&entities.UsageEvent{}).Where("id IN ?", []int64{seed[0].ID, late[0].ID}).Update("cost_usd", 7.5).Error; err != nil {
				t.Fatal(err)
			}
			resume()
			if tc.wakeAfter {
				notifier.NotifyUsageEventsCommitted(late)
			}
			// No append with new costs may precede invalidation of the old snapshot.
			frame := readLiveTestFrame(t, reader)
			if frame.kind != "reset" || frame.data["code"] != "costs_changed" {
				t.Fatalf("expected cost reset before publishing new costs: %+v", frame)
			}
			resp.Body.Close()
			freshResp, freshReader := liveTestConnect(t, client, target, nil)
			defer freshResp.Body.Close()
			fresh := liveTestReadKind(t, freshReader, "snapshot")
			rows := fresh.data["events"].([]any)
			if len(rows) != 2 {
				t.Fatalf("missing refreshed events: %+v", fresh.data)
			}
			foundOld := false
			for _, value := range rows {
				row := value.(map[string]any)
				foundOld = foundOld || row["id"] == fmt.Sprint(seed[0].ID)
				if row["cost_usd"] != 7.5 {
					t.Fatalf("mixed cost generations in fresh snapshot: %+v", row)
				}
			}
			if !foundOld {
				t.Fatal("old visible ID missing after refresh")
			}
		})
	}
}
