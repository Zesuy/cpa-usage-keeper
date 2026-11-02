package test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
)

func TestMCPPluginKeyUsageScopeAndRedaction(t *testing.T) {
	db := openAPITestDatabase(t)
	pluginGroup := helper.PluginAPIKeyGroupKey("plugin:fixture", "sensitive-principal")
	keys := []entities.CPAAPIKey{
		{APIKey: "sensitive-native-key", KeyAlias: "bot"},
		{APIKey: pluginGroup, KeyAlias: "bot", SourceType: entities.CPAAPIKeySourcePlugin, AccessProvider: "plugin:fixture"},
		{APIKey: "deleted-key", KeyAlias: "deleted", IsDeleted: true},
	}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, key := range keys {
		event := entities.UsageEvent{EventKey: fmt.Sprintf("plugin-mcp-%d", i), APIGroupKey: key.APIKey, Model: fmt.Sprintf("model-%d", i), Timestamp: now.Add(-time.Hour), InputTokens: 100, OutputTokens: int64(100 * (i + 1)), TotalTokens: int64(200 + 100*i), LatencyMS: 1000, CostUSD: new(float64(i + 1)), CostAvailable: new(true)}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil, nil, service.NewUsageService(db, emptyPricingCatalogForTest()), nil, AuthConfig{}, nil, "", OptionalProviders{MCPAPIKey: "private-mcp-test"})
	session := connectBotMCP(t, router, "")
	duplicate := callBotTool(t, session, "get_key_usage", map[string]any{})
	text := botResultText(duplicate)
	if !duplicate.IsError || !strings.Contains(text, "Key 名称重复") || !strings.Contains(text, fmt.Sprintf("ID %d", keys[1].ID)) || strings.Contains(text, "sensitive-") || strings.Contains(text, pluginGroup) {
		t.Fatalf("unsafe or missing duplicate guidance: %s", text)
	}
	for i, key := range keys[:2] {
		result := callBotTool(t, session, "get_key_usage", map[string]any{"key_id": fmt.Sprint(key.ID)})
		text = botResultText(result)
		if result.IsError || !strings.Contains(text, fmt.Sprintf("model-%d", i)) || strings.Contains(text, fmt.Sprintf("model-%d", 1-i)) || strings.Contains(text, "sensitive-") {
			t.Fatalf("key scope changed: %s", text)
		}
	}
	global := callBotTool(t, session, "get_model_tps", map[string]any{})
	text = botResultText(global)
	if global.IsError || !strings.Contains(text, "model-0") || !strings.Contains(text, "model-1") || strings.Contains(text, "model-2") || strings.Contains(text, "sensitive-") {
		t.Fatalf("global scope or redaction changed: %s", text)
	}
	if err := db.Model(&entities.CPAAPIKey{}).Where("id = ?", keys[1].ID).Update("is_deleted", true).Error; err != nil {
		t.Fatal(err)
	}
	removed := callBotTool(t, session, "get_key_usage", map[string]any{"key_id": fmt.Sprint(keys[1].ID)})
	if !removed.IsError || !strings.Contains(botResultText(removed), "不会切换为全站查询") {
		t.Fatal("deleted plugin key fell back to global usage")
	}
}
