package test

import (
	. "cpa-usage-keeper/internal/config"
	"testing"
)

func TestMCPAPIKeyDefaultsOffAndIndependent(t *testing.T) {
	withIsolatedEnvFiles(t)
	t.Setenv("CPA_BASE_URL", "http://127.0.0.1:8317")
	t.Setenv("CPA_MANAGEMENT_KEY", "management")
	t.Setenv("QUOTA_SUMMARY_API_KEY", "quota")
	for _, tc := range []struct{ env, want string }{{"", ""}, {"  mcp-key  ", "mcp-key"}, {"   ", ""}} {
		t.Setenv("MCP_API_KEY", tc.env)
		cfg, err := LoadFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MCPAPIKey != tc.want || cfg.QuotaSummaryAPIKey != "quota" {
			t.Fatal("incorrect independent MCP configuration")
		}
	}
}
