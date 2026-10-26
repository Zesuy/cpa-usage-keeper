package botreports

import (
	"fmt"
	"strings"

	"cpa-usage-keeper/internal/repository/dto"
)

func QuotaRecoveryEvents(page dto.QuotaRecoveryEventsPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "多账号Weekly异常恢复记录\n当前检测器启动时间：%s。仅比较本进程建立的真实官方查询基线。\n", date(page.DetectorStartedAt))
	if len(page.Events) == 0 {
		b.WriteString("当前没有已记录事件；可能仍在学习基线或不足三个账号，不代表已证明没有发生恢复。\n")
	}
	for _, event := range page.Events {
		fmt.Fprintf(&b, "\n事件%s：%d个不同账号在30分钟内出现非预期Weekly恢复。\n首次发现：%s；达到门槛：%s；入库：%s。\n", event.ID, event.AccountCount, date(event.FirstDetectedAt), date(event.DetectedAt), date(event.RecordedAt))
		for _, account := range event.Accounts {
			fmt.Fprintf(&b, "- %s（%s）：剩余%g%% → %g%%，增加%g个百分点；Weekly重置从%s延后到%s（延后%d秒）。\n  可能发生区间：%s—%s。\n", label(account.AccountRef), label(account.Plan), account.Before.RemainingFraction*100, account.After.RemainingFraction*100, account.RemainingIncreasePoints, date(account.Before.ResetAt), date(account.After.ResetAt), account.ResetDelaySeconds, date(account.OccurredBetween.From), date(account.OccurredBetween.To))
		}
	}
	fmt.Fprintf(&b, "\n下一游标：%s；还有下一页：%t。\n", page.NextAfterID, page.HasMore)
	b.WriteString("口径：两次相邻官方观测A→B即可命中，30分钟内至少三个不同AccountID才记录。已排除本进程检测区间内Keeper主动重置；无法排除外部客户端用卡，不认定为官方补偿。区间和发现时间不代表上游精确发生时刻；内存屏障不能追溯跨重启的旧重置请求。只读查询不会刷新额度或推进检测状态。\n")
	return b.String()
}
