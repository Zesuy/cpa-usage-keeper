package test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/auth"
	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/service"
)

const externalSummaryTestKey = "read-only-summary-test-key"

func externalSummaryRequest(router http.Handler, method, path, authorization string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if method != http.MethodGet {
		request.Header.Set("X-CPA-Usage-Keeper-Request", "fetch")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestExternalQuotaSummaryRequiresItsOwnKeyEvenWithWebAuthDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, basePath := range []string{"", "/keeper"} {
			t.Run(basePath+"/web-auth-"+map[bool]string{true: "on", false: "off"}[enabled], func(t *testing.T) {
				provider := &summaryProviderStub{}
				config := AuthConfig{Enabled: enabled, LoginPassword: "admin-password", SessionTTL: time.Hour, BasePath: basePath}
				sessions := auth.NewSessionManager(time.Hour)
				adminToken, _, err := sessions.Create()
				if err != nil {
					t.Fatal(err)
				}
				cookie := &http.Cookie{Name: standardSessionCookieName, Value: adminToken}
				router := NewRouter(nil, nil, nil, nil, config, NewAuthHandler(config, sessions), basePath, OptionalProviders{Quota: provider, QuotaSummaryAPIKey: externalSummaryTestKey})
				endpoint := basePath + "/api/v1/external/quota/summary"
				for _, test := range []struct{ name, authorization, query string }{
					{"missing", "", ""},
					{"wrong", "Bearer wrong-key", ""},
					{"admin-password", "Bearer admin-password", ""},
					{"basic", "Basic " + externalSummaryTestKey, ""},
					{"bare-key", externalSummaryTestKey, ""},
					{"extra-field", "Bearer " + externalSummaryTestKey + " extra", ""},
					{"query-only", "", "?api_key=" + externalSummaryTestKey + "&token=" + externalSummaryTestKey},
				} {
					t.Run(test.name, func(t *testing.T) {
						response := externalSummaryRequest(router, http.MethodGet, endpoint+test.query, test.authorization, cookie)
						if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header().Get("WWW-Authenticate"), "Bearer") || strings.Contains(response.Body.String(), externalSummaryTestKey) {
							t.Fatalf("unexpected auth response: %d %s", response.Code, response.Body.String())
						}
					})
				}
				if provider.reads != 0 {
					t.Fatal("unauthorized request reached quota service")
				}
				for _, authorization := range []string{"Bearer " + externalSummaryTestKey, "bearer " + externalSummaryTestKey} {
					response := externalSummaryRequest(router, http.MethodGet, endpoint, authorization, nil)
					if response.Code != http.StatusOK || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Access-Control-Allow-Origin") != "" {
						t.Fatalf("summary key did not authenticate independently: %d %s", response.Code, response.Body.String())
					}
				}
				if provider.refreshes != 0 {
					t.Fatal("read triggered upstream refresh")
				}
				// Repeated Authorization fields must not be accepted ambiguously.
				request := httptest.NewRequest(http.MethodGet, endpoint, nil)
				request.Header.Add("Authorization", "Bearer "+externalSummaryTestKey)
				request.Header.Add("Authorization", "Bearer wrong-key")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Fatal("duplicate authorization accepted")
				}
			})
		}
	}
}

func TestExternalQuotaSummaryDisabledAndKeyRotation(t *testing.T) {
	provider := &summaryProviderStub{}
	for _, key := range []string{"", "  ", "rotated-key"} {
		router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider, QuotaSummaryAPIKey: key})
		response := externalSummaryRequest(router, http.MethodGet, "/api/v1/external/quota/summary", "Bearer "+externalSummaryTestKey, nil)
		want := http.StatusNotFound
		if strings.TrimSpace(key) != "" {
			want = http.StatusUnauthorized
		}
		if response.Code != want || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("disabled/rotated key: %d", response.Code)
		}
	}
	if provider.reads != 0 {
		t.Fatal("disabled/old key accessed quota data")
	}
}

func TestExternalQuotaSummaryKeyDoesNotGrantAdministrativeOrMutationAccess(t *testing.T) {
	provider := &summaryProviderStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{Enabled: true, LoginPassword: "admin-password"}, nil, "", OptionalProviders{Quota: provider, QuotaSummaryAPIKey: externalSummaryTestKey})
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/quota/summary"},
		{http.MethodPost, "/api/v1/quota/summary/refresh"},
		{http.MethodGet, "/api/v1/usage/identities"},
		{http.MethodPost, "/api/v1/quota/reset"},
	} {
		response := externalSummaryRequest(router, test.method, test.path, "Bearer "+externalSummaryTestKey, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("key gained management access: %s %s: %d", test.method, test.path, response.Code)
		}
	}
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/external/quota/summary"},
		{http.MethodPost, "/api/v1/external/quota/summary/refresh"},
		{http.MethodGet, "/api/v1/external/quota/cache"},
		{http.MethodOptions, "/api/v1/external/quota/summary"},
	} {
		response := externalSummaryRequest(router, test.method, test.path, "Bearer "+externalSummaryTestKey, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unexpected extra external route: %s %s: %d", test.method, test.path, response.Code)
		}
	}
	if provider.reads != 0 || provider.refreshes != 0 {
		t.Fatal("unauthorized/mutation request reached provider")
	}
}

func TestExternalQuotaSummaryMatchesInternalResponseAndHandlesErrors(t *testing.T) {
	provider := &summaryProviderStub{}
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{Quota: provider, QuotaSummaryAPIKey: externalSummaryTestKey})
	internal := serveAPIGet(router, "/api/v1/quota/summary")
	external := externalSummaryRequest(router, http.MethodGet, "/api/v1/external/quota/summary", "Bearer "+externalSummaryTestKey, nil)
	if external.Code != http.StatusOK || internal.Body.String() != external.Body.String() {
		t.Fatalf("external result differs from existing summary: %s vs %s", internal.Body.String(), external.Body.String())
	}
	var result quota.SummaryResponse
	if err := json.Unmarshal(external.Body.Bytes(), &result); err != nil || len(result.Groups) != 1 || result.Groups[0].Stats.TotalRequests != 7 {
		t.Fatal("summary schema changed", err)
	}
	provider.err = errors.New("private fixture details")
	external = externalSummaryRequest(router, http.MethodGet, "/api/v1/external/quota/summary", "Bearer "+externalSummaryTestKey, nil)
	if external.Code != http.StatusInternalServerError || strings.Contains(external.Body.String(), "private") || external.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("provider error leaked details", external.Body.String())
	}
	missing := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "", OptionalProviders{QuotaSummaryAPIKey: externalSummaryTestKey})
	if response := externalSummaryRequest(missing, http.MethodGet, "/api/v1/external/quota/summary", "Bearer "+externalSummaryTestKey, nil); response.Code != http.StatusInternalServerError {
		t.Fatal("missing provider not handled")
	}
}

func TestExternalQuotaSummaryAuthenticatesBeforeCostReadGate(t *testing.T) {
	provider := &summaryProviderStub{}
	gate := service.NewCostReadGate()
	resume, err := gate.BlockAndDrain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resume)
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "/keeper", OptionalProviders{Quota: provider, CostReadGate: gate, QuotaSummaryAPIKey: externalSummaryTestKey})
	path := "/keeper/api/v1/external/quota/summary"
	if response := externalSummaryRequest(router, http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatal("cost gate ran before key validation")
	}
	response := externalSummaryRequest(router, http.MethodGet, path, "Bearer "+externalSummaryTestKey, nil)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"costs_busy"`) || response.Header().Get("Cache-Control") != "no-store" || provider.reads != 0 {
		t.Fatalf("fee reads not blocked: %d %s", response.Code, response.Body.String())
	}
	resume()
	if response = externalSummaryRequest(router, http.MethodGet, path, "Bearer "+externalSummaryTestKey, nil); response.Code != http.StatusOK {
		t.Fatal("fee gate did not recover", response.Code)
	}
}
