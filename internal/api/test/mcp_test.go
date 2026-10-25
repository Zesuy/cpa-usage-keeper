package test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type botUsageStub struct {
	service.UsageProvider
	reads atomic.Int32
	read  func(context.Context, repository.BotUsageQuery, time.Time) (*repository.BotUsageReport, error)
}

func (s *botUsageStub) GetBotUsage(ctx context.Context, q repository.BotUsageQuery, now time.Time) (*repository.BotUsageReport, error) {
	s.reads.Add(1)
	if s.read != nil {
		return s.read(ctx, q, now)
	}
	return &repository.BotUsageReport{Start: now.Add(-24 * time.Hour), End: now, KeyID: 42, KeyLabel: "bot", Total: repository.BotUsageMetrics{CostAvailable: true}}, nil
}

type mcpAuthRoundTripper struct {
	key string
	t   *testing.T
}

func (tr mcpAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+tr.key)
	if copy.Header.Get("X-CPA-Usage-Keeper-Request") != "" {
		tr.t.Error("client unexpectedly requires webpage headers")
	}
	res, err := http.DefaultTransport.RoundTrip(copy)
	if err == nil && (res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Set-Cookie") != "" || res.Header.Get("Mcp-Session-Id") != "") {
		tr.t.Errorf("unexpected MCP headers: %v", res.Header)
	}
	return res, err
}

func connectBotMCP(t *testing.T, handler http.Handler, basePath string, options ...*mcp.ClientSessionOptions) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "bot-test", Version: "1.0.0"}, nil)
	var opts *mcp.ClientSessionOptions
	if len(options) > 0 {
		opts = options[0]
	}
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + basePath + "/mcp", HTTPClient: &http.Client{Transport: mcpAuthRoundTripper{"private-mcp-test", t}}, DisableStandaloneSSE: true, MaxRetries: -1}, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callBotTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func botResultText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range result.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func TestMCPIndependentBearerAndDisabledRoute(t *testing.T) {
	for _, base := range []string{"", "/keeper"} {
		for _, enabled := range []bool{false, true} {
			provider := &botUsageStub{}
			router := NewRouter(nil, nil, provider, nil, AuthConfig{Enabled: enabled, LoginPassword: "admin"}, nil, base, OptionalProviders{MCPAPIKey: "private-mcp-test", QuotaSummaryAPIKey: "quota-only"})
			for _, authorization := range []string{"", "Bearer wrong", "Bearer quota-only", "Basic private-mcp-test", "Bearer admin"} {
				req := httptest.NewRequest(http.MethodPost, base+"/mcp?api_key=private-mcp-test", nil)
				req.Header.Set("Authorization", authorization)
				req.AddCookie(&http.Cookie{Name: standardSessionCookieName, Value: "session"})
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)
				if res.Code != 401 || res.Header().Get("Cache-Control") != "no-store" || strings.Contains(res.Body.String(), "private-mcp-test") {
					t.Fatalf("MCP auth bypass: %d %s", res.Code, res.Body.String())
				}
			}
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				req := httptest.NewRequest(method, base+"/mcp", nil)
				req.Header.Set("Authorization", "Bearer private-mcp-test")
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)
				if res.Code != 405 || res.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("stateless methods", res.Code)
				}
			}
			req := httptest.NewRequest(http.MethodPost, base+"/mcp", nil)
			req.Header.Add("Authorization", "Bearer private-mcp-test")
			req.Header.Add("Authorization", "Bearer private-mcp-test")
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != 401 {
				t.Fatal("duplicate auth accepted")
			}
			session := connectBotMCP(t, router, base)
			tools, err := session.ListTools(context.Background(), nil)
			if err != nil || len(tools.Tools) != 3 {
				t.Fatal("tool discovery", err)
			}
			for _, tool := range tools.Tools {
				if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.OutputSchema != nil {
					t.Fatalf("tool should be read-only text: %+v", tool)
				}
			}
			if provider.reads.Load() != 0 {
				t.Fatal("initialization read usage")
			}
			if result := callBotTool(t, session, "get_key_usage", map[string]any{}); result.IsError || !strings.Contains(botResultText(result), "Key使用报告：bot") {
				t.Fatal(botResultText(result))
			}
		}
	}
	for _, key := range []string{"", "  "} {
		router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{MCPAPIKey: key})
		res := externalSummaryRequest(router, http.MethodGet, "/mcp", "Bearer private-mcp-test", nil)
		if res.Code != 404 || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("disabled MCP", res.Code)
		}
	}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{Enabled: true, LoginPassword: "admin"}, nil, "", OptionalProviders{MCPAPIKey: "private-mcp-test", QuotaSummaryAPIKey: "quota-only"})
	for _, path := range []string{"/api/v1/usage/overview?range=24h", "/api/v1/quota/summary", "/api/v1/usage/api-keys/settings", "/api/v1/external/quota/summary"} {
		if res := externalSummaryRequest(router, http.MethodGet, path, "Bearer private-mcp-test", nil); res.Code != 401 {
			t.Fatalf("MCP key gained unrelated access %s: %d", path, res.Code)
		}
	}
}

func TestMCPToolsValidateScopeAndReportFailures(t *testing.T) {
	provider := &botUsageStub{}
	var last repository.BotUsageQuery
	provider.read = func(ctx context.Context, q repository.BotUsageQuery, now time.Time) (*repository.BotUsageReport, error) {
		last = q
		return &repository.BotUsageReport{Start: now.Add(-24 * time.Hour), End: now, KeyLabel: "bot", Total: repository.BotUsageMetrics{CostAvailable: true}}, nil
	}
	quotaProvider := &summaryProviderStub{}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "", OptionalProviders{MCPAPIKey: "private-mcp-test", Quota: quotaProvider})
	session := connectBotMCP(t, router, "")
	if r := callBotTool(t, session, "get_key_usage", map[string]any{}); r.IsError || !last.KeyScoped || last.KeyName != "bot" {
		t.Fatal("default scope", last)
	}
	callBotTool(t, session, "get_key_usage", map[string]any{"key_id": "42"})
	if !last.KeyScoped || last.KeyID != 42 || last.KeyName != "" {
		t.Fatal("ID scope", last)
	}
	callBotTool(t, session, "get_model_tps", map[string]any{"models": []string{"a", "a"}, "range": "24h", "granularity": "hour"})
	if last.KeyScoped || len(last.Models) != 1 || last.Models[0] != "a" {
		t.Fatal("global TPS scope", last)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"get_key_usage", map[string]any{"key_name": "bot", "key_id": "42"}},
		{"get_key_usage", map[string]any{"key_name": " "}},
		{"get_key_usage", map[string]any{"key_id": "9223372036854775808"}},
		{"get_key_usage", map[string]any{"range": "1d"}},
		{"get_model_tps", map[string]any{"key_id": "42"}},
		{"get_model_tps", map[string]any{"models": []string{" "}}},
		{"get_quota_summary", map[string]any{"pools": []string{"unknown"}}},
	} {
		before := provider.reads.Load()
		r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && !r.IsError {
			t.Fatalf("invalid args accepted: %+v", tc)
		}
		if provider.reads.Load() != before {
			t.Fatal("invalid args reached storage")
		}
	}
	r := callBotTool(t, session, "get_quota_summary", map[string]any{"pools": []string{"codex"}})
	if r.IsError || !strings.Contains(botResultText(r), "Codex / GPT") || quotaProvider.reads != 1 || quotaProvider.refreshes != 0 || r.StructuredContent != nil {
		t.Fatal("quota tool should directly read text snapshot", botResultText(r))
	}
	provider.read = func(context.Context, repository.BotUsageQuery, time.Time) (*repository.BotUsageReport, error) {
		return nil, &repository.BotKeyError{Candidates: []repository.BotKeyCandidate{{ID: 42, Label: "bot", MaskedKey: "sk-...1234"}}}
	}
	if r = callBotTool(t, session, "get_key_usage", map[string]any{}); !r.IsError || !strings.Contains(botResultText(r), "ID 42") {
		t.Fatal("missing duplicate guidance")
	}
	provider.read = func(context.Context, repository.BotUsageQuery, time.Time) (*repository.BotUsageReport, error) {
		return nil, errors.New("private sql and secret")
	}
	if r = callBotTool(t, session, "get_key_usage", map[string]any{}); !r.IsError || strings.Contains(botResultText(r), "private") {
		t.Fatal("provider error leaked")
	}
	quotaProvider.err = errors.New("private summary")
	if r = callBotTool(t, session, "get_quota_summary", map[string]any{}); !r.IsError || strings.Contains(botResultText(r), "private") {
		t.Fatal("quota error leaked")
	}
}

func TestMCPCostGateAndCancellation(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) { testMCPCostGateAndCancellation(t, version) })
	}
}

func testMCPCostGateAndCancellation(t *testing.T, version string) {
	provider := &botUsageStub{}
	gate := service.NewCostReadGate()
	resume, err := gate.BlockAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resume)
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "/keeper", OptionalProviders{MCPAPIKey: "private-mcp-test", CostReadGate: gate})
	session := connectBotMCP(t, router, "/keeper", &mcp.ClientSessionOptions{ProtocolVersion: version})
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal("blocked gate broke discovery", err)
	}
	for _, name := range []string{"get_key_usage", "get_model_tps", "get_quota_summary"} {
		r := callBotTool(t, session, name, map[string]any{})
		if !r.IsError || !strings.Contains(botResultText(r), "costs_busy") {
			t.Fatal("gate was bypassed", botResultText(r))
		}
	}
	if provider.reads.Load() != 0 {
		t.Fatal("blocked provider read")
	}
	resume()
	if r := callBotTool(t, session, "get_key_usage", map[string]any{}); r.IsError {
		t.Fatal("gate failed to recover")
	}
	done := make(chan struct{})
	started := make(chan struct{})
	fallback := make(chan struct{})
	defer close(fallback)
	provider.read = func(ctx context.Context, _ repository.BotUsageQuery, _ time.Time) (*repository.BotUsageReport, error) {
		close(started)
		select {
		case <-ctx.Done():
		case <-fallback:
		}
		close(done)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_key_usage", Arguments: map[string]any{}})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("query did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not reach provider")
	}
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not cancel")
	}
	// A finished/cancelled read must release its cost permit.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	release, err := gate.BlockAndDrain(ctx2)
	if err != nil {
		t.Fatal("cost permit leaked", err)
	}
	release()
}

func TestMCPRealUsageServiceReadsStoredValues(t *testing.T) {
	db := openAPITestDatabase(t)
	keys := []entities.CPAAPIKey{{APIKey: "private-bot-key", KeyAlias: "bot"}, {APIKey: "private-other-key", KeyAlias: "other"}}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, key := range keys {
		e := entities.UsageEvent{EventKey: key.KeyAlias, APIGroupKey: key.APIKey, Model: "test-model", Timestamp: now.Add(-2 * time.Hour), InputTokens: 100, OutputTokens: 200, TotalTokens: 300, CacheReadTokens: 80, LatencyMS: int64(1000 * (i + 1)), CostUSD: new(1.5), CostAvailable: new(true)}
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil, nil, service.NewUsageService(db, emptyPricingCatalogForTest()), nil, AuthConfig{}, nil, "", OptionalProviders{MCPAPIKey: "private-mcp-test"})
	session := connectBotMCP(t, router, "")
	r := callBotTool(t, session, "get_key_usage", map[string]any{})
	text := botResultText(r)
	for _, want := range []string{"请求1次", "总Token：300", "缓存读取率：80.00%", "费用：1.50 USD", "200.00 | 1"} {
		if r.IsError || !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "private-") {
		t.Fatal("full key leaked")
	}
	r = callBotTool(t, session, "get_model_tps", map[string]any{})
	if text = botResultText(r); r.IsError || !strings.Contains(text, "全时段平均：150.00 TPS；有效样本数：2") {
		t.Fatal(text)
	}
}
