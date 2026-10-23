package test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/quota"
)

type summaryProviderStub struct {
	quotaProviderStub
	reads, refreshes int
	err              error
}

func (s *summaryProviderStub) GetQuotaSummary(context.Context) (quota.SummaryResponse, error) {
	s.reads++
	return quota.SummaryResponse{Eligible: 3, Groups: []quota.SummaryGroup{{Provider: "codex", Pool: "codex", Label: "Codex / GPT", Quota: []quota.SummaryRow{}}}}, s.err
}
func (s *summaryProviderStub) RefreshQuotaSummary(context.Context) (quota.RefreshResponse, error) {
	s.refreshes++
	return quota.RefreshResponse{Accepted: 3, Limit: 3}, s.err
}
func TestQuotaSummaryReadDoesNotRefreshAndRefreshIsExplicit(t *testing.T) {
	provider := &summaryProviderStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider})
	response := serveAPIGet(router, "/api/v1/quota/summary")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"eligible":3`) || !strings.Contains(response.Body.String(), `"pool":"codex"`) {
		t.Fatalf("summary: %d %s", response.Code, response.Body.String())
	}
	if provider.reads != 1 || provider.refreshes != 0 {
		t.Fatal("read refreshed upstream")
	}
	response = serveCredentialMutation(router, http.MethodPost, "/api/v1/quota/summary/refresh", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accepted":3`) || provider.refreshes != 1 {
		t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
	}
	provider.err = errors.New("fixture error")
	if response = serveAPIGet(router, "/api/v1/quota/summary"); response.Code != http.StatusInternalServerError {
		t.Fatal(response.Code)
	}
	if response = serveCredentialMutation(router, http.MethodPost, "/api/v1/quota/summary/refresh", ""); response.Code != http.StatusInternalServerError {
		t.Fatal(response.Code)
	}
}
