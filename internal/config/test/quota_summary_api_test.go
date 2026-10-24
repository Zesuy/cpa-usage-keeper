package test

import (
	"testing"

	. "cpa-usage-keeper/internal/config"
)

func TestQuotaSummaryAPIKeyDefaultsOffAndLoadsOnlyItsOwnEnv(t *testing.T) {
	withIsolatedEnvFiles(t)
	t.Setenv("CPA_BASE_URL", "http://127.0.0.1:8317")
	t.Setenv("CPA_MANAGEMENT_KEY", "cpa-management-secret")
	for _, value := range []string{"", "  private-summary-key  ", "   "} {
		t.Setenv("QUOTA_SUMMARY_API_KEY", value)
		cfg, err := LoadFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if value == "  private-summary-key  " {
			want = "private-summary-key"
		}
		if cfg.QuotaSummaryAPIKey != want {
			t.Fatal("incorrect quota summary key configuration")
		}
	}
}
