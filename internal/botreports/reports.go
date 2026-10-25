// Package botreports renders local read-only snapshots for an AI to read directly.
package botreports

import (
	"fmt"
	"math"
	"strings"
	"time"

	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"
)

func label(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == '|' {
			return ' '
		}
		return r
	}, s)
}
func date(t time.Time) string {
	return timeutil.NormalizeStorageTime(t).Format("2006-01-02 15:04:05.999999999 -07:00")
}
func hour(start, end time.Time) string {
	return timeutil.NormalizeStorageTime(start).Format("01-02 15:04:05.999999999 -07:00") + "—" + timeutil.NormalizeStorageTime(end).Format("01-02 15:04:05.999999999 -07:00")
}
func rate(n, d int64) string {
	if d <= 0 {
		return "无法计算"
	}
	return fmt.Sprintf("%.2f%%", float64(n)/float64(d)*100)
}
func mean(m repository.BotUsageMetrics) string {
	if m.SpeedSampleCount <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", m.SpeedTPSSum/float64(m.SpeedSampleCount))
}
func cost(m repository.BotUsageMetrics) string {
	if !m.CostAvailable {
		return "无法完整计算（缺少价格或费用数据）"
	}
	return usd(m.CostUSD)
}

// Keep sub-cent usage visible instead of turning a small charge into a false zero.
func usd(n float64) string {
	if n != 0 && math.Abs(n) < 0.00000001 {
		return fmt.Sprintf("%.6g", n)
	}
	s := strings.TrimRight(fmt.Sprintf("%.8f", n), "0")
	if strings.HasSuffix(s, ".") {
		return s + "00"
	}
	if len(s)-strings.LastIndex(s, ".") == 2 {
		s += "0"
	}
	return s
}

func heading(b *strings.Builder, title string, r *repository.BotUsageReport) {
	fmt.Fprintf(b, "%s\n统计范围：%s—%s；严格最近24小时，结束时刻不包含在内。\n时区：%s；首尾不足一小时按实际边界显示。\n", title, date(r.Start), date(r.End), r.End.Location().String())
}

func KeyUsage(r *repository.BotUsageReport) string {
	var b strings.Builder
	heading(&b, "Key使用报告："+label(r.KeyLabel)+fmt.Sprintf("（ID %d）", r.KeyID), r)
	m := r.Total
	fmt.Fprintf(&b, "\n总览\n请求%d次：成功%d次，失败%d次，成功率%s。\n总Token：%d；输入：%d；输出：%d；推理：%d。\n缓存读取：%d；缓存写入：%d；缓存读取率：%s。\nKeeper测算费用：%s USD。\n", m.Requests, m.Success, m.Failure, rate(m.Success, m.Requests), m.TotalTokens, m.InputTokens, m.OutputTokens, m.ReasoningTokens, m.CacheReadTokens, m.CacheCreationTokens, rate(m.CacheReadTokens, m.InputTokens), cost(m))
	if m.Requests == 0 {
		b.WriteString("该时段无请求；无有效样本的比率和TPS不视为零。\n")
	}
	b.WriteString("\n模型分布\n模型 | 请求数 | Token | Token占比 | 费用USD | 平均TPS | 有效速度样本数\n")
	for _, model := range r.Models {
		fmt.Fprintf(&b, "%s | %d | %d | %s | %s | %s | %d\n", label(model.Model), model.Requests, model.TotalTokens, rate(model.TotalTokens, m.TotalTokens), cost(model.BotUsageMetrics), mean(model.BotUsageMetrics), model.SpeedSampleCount)
	}
	b.WriteString("\n逐小时用量\n时间范围 | 请求 | 成功 | 失败 | 输入Token | 输出Token | 缓存读取Token | 缓存写入Token | 总Token | 费用USD\n")
	for _, h := range r.Hours {
		fmt.Fprintf(&b, "%s | %d | %d | %d | %d | %d | %d | %d | %d | %s\n", hour(h.Start, h.End), h.Requests, h.Success, h.Failure, h.InputTokens, h.OutputTokens, h.CacheReadTokens, h.CacheCreationTokens, h.TotalTokens, cost(h.BotUsageMetrics))
	}
	b.WriteString("\n说明：费用按Keeper已存价格规则测算，不代表供应商实际账单。缓存读取率=缓存读取Token/输入Token。TPS为逐请求输出Token/总耗时秒数的算术平均，跨小时按有效样本数合并。查询时间不表示上游已经同步完成。\n")
	return b.String()
}

func ModelTPS(r *repository.BotUsageReport, requested []string) string {
	var b strings.Builder
	heading(&b, "全站模型TPS报告（不按Key筛选，沿用Keeper有效Key范围）", r)
	seen := map[string]bool{}
	for _, model := range r.Models {
		seen[model.Model] = true
		fmt.Fprintf(&b, "\n%s\n全时段平均：%s TPS；有效样本数：%d。\n", label(model.Model), mean(model.BotUsageMetrics), model.SpeedSampleCount)
		low, high := -1, -1
		for i, h := range model.Hours {
			if h.SpeedSampleCount <= 0 {
				continue
			}
			x := h.SpeedTPSSum / float64(h.SpeedSampleCount)
			if low < 0 || x < model.Hours[low].SpeedTPSSum/float64(model.Hours[low].SpeedSampleCount) {
				low = i
			}
			if high < 0 || x > model.Hours[high].SpeedTPSSum/float64(model.Hours[high].SpeedSampleCount) {
				high = i
			}
		}
		if low < 0 {
			b.WriteString("小时均值变化范围：—（无有效速度样本）。\n")
		} else {
			fmt.Fprintf(&b, "小时均值变化范围：%s—%s TPS。\n最低小时：%s，%s TPS。\n最高小时：%s，%s TPS。\n", mean(model.Hours[low]), mean(model.Hours[high]), hour(r.Hours[low].Start, r.Hours[low].End), mean(model.Hours[low]), hour(r.Hours[high].Start, r.Hours[high].End), mean(model.Hours[high]))
		}
		b.WriteString("时间范围 | 平均TPS | 有效速度样本数\n")
		for i, h := range model.Hours {
			fmt.Fprintf(&b, "%s | %s | %d\n", hour(r.Hours[i].Start, r.Hours[i].End), mean(h), h.SpeedSampleCount)
		}
	}
	if len(r.Models) == 0 {
		b.WriteString("\n该时段没有匹配模型的请求记录。\n")
	}
	for _, model := range requested {
		if !seen[model] {
			fmt.Fprintf(&b, "模型%s：该时段无请求记录。\n", label(model))
		}
	}
	b.WriteString("\n口径：TPS=每次有效请求的输出Token/总耗时秒数，再对请求取算术平均；有效条件为输出Token和总耗时均大于零，失败状态不额外排除样本。全时段平均按样本数合并；变化范围指小时均值，不是单请求极值。—表示无有效样本，不是0 TPS。查询时间不表示上游已经同步完成。\n")
	return b.String()
}

func number(n *float64) string {
	if n == nil || math.IsNaN(*n) || math.IsInf(*n, 0) {
		return "无法估算"
	}
	return fmt.Sprintf("%.0f", *n)
}
func money(n *float64) string {
	if n == nil || math.IsNaN(*n) || math.IsInf(*n, 0) {
		return "无法估算"
	}
	return usd(*n)
}
func remaining(total, used *float64) *float64 {
	if total == nil || used == nil {
		return nil
	}
	n := math.Max(*total-*used, 0)
	return &n
}
func tokenFloat(n *int64) *float64 {
	if n == nil {
		return nil
	}
	x := float64(*n)
	return &x
}

func Quota(summary quota.SummaryResponse, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Keeper聚合额度\n查询时间：%s；时区：%s。\n范围：有效供应侧凭据池，不属于某个客户端API Key；本次匹配%d个模型池。全站有效凭据%d个，刷新待处理%d个。\n", date(now), timeutil.NormalizeStorageTime(now).Location().String(), len(summary.Groups), summary.Eligible, summary.Pending)
	if !summary.GeneratedAt.IsZero() {
		fmt.Fprintf(&b, "聚合结果生成时间：%s。\n", date(summary.GeneratedAt))
	}
	if len(summary.Groups) == 0 {
		b.WriteString("当前暂无匹配的可用聚合额度，不能据此判断额度为零。\n")
	}
	for _, group := range summary.Groups {
		fmt.Fprintf(&b, "\n%s（%s）", label(group.Label), label(group.Pool))
		for _, plan := range group.Plans {
			fmt.Fprintf(&b, " %s ×%d", label(plan.Plan), plan.Count)
		}
		b.WriteString("\n")
		for _, seconds := range []int64{18000, 604800} {
			window := "5小时"
			if seconds == 604800 {
				window = "周额度"
			}
			var row *quota.SummaryRow
			for i := range group.Quota {
				q := &group.Quota[i]
				if q.Window != nil && q.Window.Seconds != nil && *q.Window.Seconds == seconds {
					row = q
					break
				}
			}
			if row == nil {
				fmt.Fprintf(&b, "- %s：无该窗口额度数据。\n", window)
				continue
			}
			fmt.Fprintf(&b, "- %s：", window)
			if row.RemainingFraction == nil {
				b.WriteString("已用和剩余比例无法估算。\n")
			} else {
				f := math.Max(0, math.Min(1, *row.RemainingFraction))
				fmt.Fprintf(&b, "已用%.2f%%，剩余%.2f%%。\n", (1-f)*100, f*100)
			}
			usedTokens := tokenFloat(row.WindowUsageTokens)
			fmt.Fprintf(&b, "  当前窗口已用：%s Token、%s USD。\n  预估总容量：%s Token、%s USD。\n  预估剩余：%s Token、%s USD。\n", number(usedTokens), money(row.WindowUsageCost), number(row.WindowUsageEstimateTokens), money(row.WindowUsageEstimateCost), number(remaining(row.WindowUsageEstimateTokens, usedTokens)), money(remaining(row.WindowUsageEstimateCost, row.WindowUsageCost)))
			c := row.Coverage
			fmt.Fprintf(&b, "  用量覆盖%d/%d；估值覆盖%d/%d（直接%d、历史%d、同套餐补值%d，补值样本%d）；未知%d。用量状态%s，估值状态%s。\n", c.Current, c.Eligible, c.Direct+c.Historical+c.Peer, c.Eligible, c.Direct, c.Historical, c.Peer, c.Samples, c.Unknown, label(row.UsageState), label(row.EstimateState))
			fmt.Fprintf(&b, "  缓存保留%d、过旧%d、跨周期%d、缺失%d、失败%d、待刷新%d、不支持%d。\n", c.Retained, c.Stale, c.Expired, c.Missing, c.Failed, c.Pending, c.Unsupported)
			if c.Current < c.Eligible || c.Direct+c.Historical+c.Peer < c.Eligible {
				b.WriteString("  注意：部分覆盖，以上金额和Token不代表已覆盖整个池。\n")
			}
			if c.OldestSnapshotAt != nil {
				fmt.Fprintf(&b, "  最早额度快照：%s。\n", date(*c.OldestSnapshotAt))
			}
			if c.OldestEstimateAt != nil {
				fmt.Fprintf(&b, "  最早估值样本：%s。\n", date(*c.OldestEstimateAt))
			}
			if c.NextResetAt == nil {
				b.WriteString("  最近重置时间：未知。\n")
			} else {
				secondsLeft := max(int64(c.NextResetAt.Sub(now).Seconds()), 0)
				fmt.Fprintf(&b, "  最近重置：%s；距查询时刻%dd%dh%dm%ds。\n", date(*c.NextResetAt), secondsLeft/86400, secondsLeft%86400/3600, secondsLeft%3600/60, secondsLeft%60)
			}
		}
	}
	b.WriteString("\n说明：Token和USD容量均为Keeper估算，USD不是实际账单。预估剩余=max(预估总容量-当前窗口已用量,0)，必要数值缺失时无法估算。额度百分比沿用Keeper聚合结果，不从美元余额反推。最早重置只代表最早一个凭据窗口，不代表整个池同时恢复；满额状态以服务端结果为准。5小时与周额度同时生效，不可相加。查询时间不代表上游已同步完成。\n")
	return b.String()
}
