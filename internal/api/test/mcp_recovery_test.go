package test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpRecoveryStub struct {
	quotaProviderStub
	reads atomic.Int32
	read  func(context.Context, int64, int) (dto.QuotaRecoveryEventsPage, error)
}

func (s *mcpRecoveryStub) GetQuotaRecoveryEvents(ctx context.Context, after int64, limit int) (dto.QuotaRecoveryEventsPage, error) {
	s.reads.Add(1)
	return s.read(ctx, after, limit)
}

func TestMCPRecoveryStructuredCursorAndErrors(t *testing.T) {
	provider := &mcpRecoveryStub{}
	provider.read = func(_ context.Context, after int64, limit int) (dto.QuotaRecoveryEventsPage, error) {
		if after != 0 || limit != 50 {
			t.Errorf("defaults %d %d", after, limit)
		}
		return dto.QuotaRecoveryEventsPage{Events: []dto.QuotaRecoveryEvent{{ID: "7", Type: "multi_account_weekly_recovery", AccountCount: 3}}, NextAfterID: "7", HasMore: true, DetectorStartedAt: time.Now().UTC()}, nil
	}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "/keeper", OptionalProviders{MCPAPIKey: "private-mcp-test", Quota: provider})
	session := connectBotMCP(t, router, "/keeper")
	if provider.reads.Load() != 0 {
		t.Fatal("init read events")
	}
	result := callBotTool(t, session, "get_quota_recovery_events", map[string]any{})
	if result.IsError || result.StructuredContent == nil || !strings.Contains(botResultText(result), "事件7") || !strings.Contains(botResultText(result), "外部客户端用卡") {
		t.Fatalf("result %+v", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var page dto.QuotaRecoveryEventsPage
	if err := json.Unmarshal(encoded, &page); err != nil || page.NextAfterID != "7" || !page.HasMore || len(page.Events) != 1 {
		t.Fatalf("structured %s %v", encoded, err)
	}
	for _, args := range []map[string]any{{"after_id": "-1"}, {"after_id": "01"}, {"after_id": "9223372036854775808"}, {"after_id": 5}, {"limit": 0}, {"limit": 201}, {"extra": true}} {
		before := provider.reads.Load()
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_quota_recovery_events", Arguments: args})
		if err == nil && !r.IsError {
			t.Fatalf("accepted %+v", args)
		}
		if provider.reads.Load() != before {
			t.Fatal("invalid input reached DB")
		}
	}
	provider.read = func(_ context.Context, after int64, limit int) (dto.QuotaRecoveryEventsPage, error) {
		if after != 7 || limit != 200 {
			t.Errorf("cursor %d %d", after, limit)
		}
		return dto.QuotaRecoveryEventsPage{Events: []dto.QuotaRecoveryEvent{}, NextAfterID: "7", DetectorStartedAt: time.Now().UTC()}, nil
	}
	result = callBotTool(t, session, "get_quota_recovery_events", map[string]any{"after_id": "7", "limit": 200})
	if result.IsError || !strings.Contains(botResultText(result), "不代表已证明没有发生恢复") {
		t.Fatal(botResultText(result))
	}
	for _, expected := range []struct {
		err  error
		text string
	}{{repository.ErrQuotaRecoveryCursor, "invalid_arguments"}, {errors.New("private database secret"), "read_failed"}} {
		provider.read = func(context.Context, int64, int) (dto.QuotaRecoveryEventsPage, error) {
			return dto.QuotaRecoveryEventsPage{}, expected.err
		}
		result = callBotTool(t, session, "get_quota_recovery_events", map[string]any{})
		if !result.IsError || result.StructuredContent != nil || !strings.Contains(botResultText(result), expected.text) || strings.Contains(botResultText(result), "private") {
			t.Fatal(botResultText(result))
		}
	}
}
