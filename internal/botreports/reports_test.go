package botreports

import (
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
)

func TestModelTPSMeansAndHourRanges(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	r := &repository.BotUsageReport{Start: now.Add(-24 * time.Hour), End: now}
	m := repository.BotModelUsage{Model: "model", BotUsageMetrics: repository.BotUsageMetrics{SpeedTPSSum: 280, SpeedSampleCount: 4}}
	for i := 0; i < 24; i++ {
		r.Hours = append(r.Hours, repository.BotUsageHour{Start: r.Start.Add(time.Duration(i) * time.Hour), End: r.Start.Add(time.Duration(i+1) * time.Hour)})
		m.Hours = append(m.Hours, repository.BotUsageMetrics{})
	}
	m.Hours[0] = repository.BotUsageMetrics{SpeedTPSSum: 240, SpeedSampleCount: 3}
	m.Hours[1] = repository.BotUsageMetrics{SpeedTPSSum: 40, SpeedSampleCount: 1}
	r.Models = []repository.BotModelUsage{m}
	text := ModelTPS(r, []string{"model", "absent"})
	for _, want := range []string{"全时段平均：70.00 TPS", "小时均值变化范围：40.00—80.00 TPS", "— | 0", "模型absent：该时段无请求记录", "不是单请求极值"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	if strings.Count(text, " | ") != 50 {
		t.Fatal("hours compressed")
	}
	r.Total.CostAvailable = false
	text = KeyUsage(r)
	if !strings.Contains(text, "费用：无法完整计算") || !strings.Contains(text, "该时段无请求") || !strings.Contains(text, "缓存读取率：无法计算") {
		t.Fatal(text)
	}
}

func TestQuotaAmountsUnknownZeroCoverageAndReset(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	row := quota.SummaryRow{QuotaRow: quota.QuotaRow{Window: &quota.QuotaWindow{Seconds: new(int64(18000))}, RemainingFraction: new(0.7), WindowUsageTokens: new(int64(10)), WindowUsageCost: new(34.68), WindowUsageEstimateTokens: new(100.0), WindowUsageEstimateCost: new(80.0)}, Coverage: quota.SummaryCoverage{Eligible: 3, Current: 2, Direct: 1, Historical: 1, Peer: 1, Retained: 1, Stale: 1, NextResetAt: new(now.Add(-time.Minute))}, UsageState: "cached", EstimateState: "cached"}
	summary := quota.SummaryResponse{GeneratedAt: now, Eligible: 3, Groups: []quota.SummaryGroup{{Pool: "codex", Label: "Codex", Plans: []quota.SummaryPlanCount{{Plan: "team", Count: 3}}, Quota: []quota.SummaryRow{row}}}}
	text := Quota(summary, now)
	for _, want := range []string{"剩余70.00%", "预估剩余：90 Token、45.32 USD", "部分覆盖", "缓存保留1、过旧1", "0d0h0m0s", "周额度：无该窗口额度数据", "team ×3"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	row.WindowUsageCost = new(100.0)
	row.WindowUsageTokens = new(int64(200))
	summary.Groups[0].Quota[0] = row
	if text = Quota(summary, now); !strings.Contains(text, "预估剩余：0 Token、0.00 USD") {
		t.Fatal(text)
	}
	row.WindowUsageEstimateCost = nil
	row.WindowUsageEstimateTokens = nil
	row.RemainingFraction = new(1.0)
	row.UsageState = "reset"
	summary.Groups[0].Quota[0] = row
	if text = Quota(summary, now); !strings.Contains(text, "剩余100.00%") || !strings.Contains(text, "预估剩余：无法估算 Token、无法估算 USD") {
		t.Fatal(text)
	}
	if text = Quota(quota.SummaryResponse{}, now); !strings.Contains(text, "不能据此判断额度为零") {
		t.Fatal(text)
	}
}

func TestReportsPreserveSmallCostsAndExactBoundaries(t *testing.T) {
	for _, n := range []float64{0.000004, 0.0000000001} {
		if usd(n) == "0.00" || usd(n) == "0" {
			t.Fatalf("nonzero charge lost: %g", n)
		}
	}
	if usd(0) != "0.00" || usd(1.5) != "1.50" {
		t.Fatal("zero/standard fee formatting")
	}
	start := time.Date(2026, 10, 25, 12, 30, 45, 123, time.Local)
	if !strings.Contains(hour(start, start.Add(time.Minute)), "12:30:45.000000123") {
		t.Fatal("partial-hour boundary lost precision")
	}
}
