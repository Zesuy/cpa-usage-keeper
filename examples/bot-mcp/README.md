# Bot 只读 MCP

Keeper 提供三个面向 AI 的只读工具，结果放在 MCP `content` 的文本块中，默认使用中文报告。无需浏览器登录、Cookie 或 Keeper 网页专用请求头，不提供管理、刷新、Key 排行或实时监控工具。

## 部署与鉴权

在 Keeper 容器环境中设置独立随机密钥，例如用 `openssl rand -hex 32` 生成后保存到私有 env 文件：

```env
MCP_API_KEY=<独立随机密钥>
```

重建容器后生效。Docker Compose 的 `environment` 或 `env_file` 必须实际把变量传入容器；只修改宿主机 `.env` 不一定会自动传入。生产容器由部署者更新。

连接地址为 `https://keeper.example.com{APP_BASE_PATH}/mcp`。根路径部署使用 `/mcp`，`APP_BASE_PATH=/keeper` 时使用 `/keeper/mcp`。传输为无状态 Streamable HTTP，POST 返回 JSON，不创建持久 MCP 会话或网页登录会话。反向代理应透传 `Authorization`、MCP 协议头及 POST 响应。

每个请求必须携带：

```http
Authorization: Bearer <MCP_API_KEY>
```

密钥留空返回 `404`；缺失或错误返回 `401`。不接受 URL 参数或 Cookie 中的密钥。`AUTH_ENABLED=false` 也不会免除 MCP 鉴权。`QUOTA_SUMMARY_API_KEY` 仅能访问原有聚合额度 REST 接口；MCP 密钥不会因此获得其他 REST 或管理接口权限。所有响应为 `Cache-Control: no-store`。

此密钥可以读取下述三个工具覆盖的数据，包含其他有效 Key 的指定用量和全站模型速度；它不是绑定到 `bot` 的受限 Key。仅交给可信 Bot 的宿主进程，勿放入 HTML、浏览器存储或提交到仓库。优先通过 HTTPS 连接。

支持静态请求头的 Bot 客户端可以按其配置格式填写以下内容（占位符须由宿主的私有配置/密钥管理替换）：

```json
{
  "mcpServers": {
    "keeper": {
      "type": "http",
      "url": "https://keeper.example.com/keeper/mcp",
      "headers": { "Authorization": "Bearer <MCP_API_KEY>" }
    }
  }
}
```

客户端配置字段因 Bot 而异。Keeper 使用官方 Go MCP SDK v1.8.0；以下为同一 SDK 的最小宿主调用方式，环境变量只在宿主读取：

```go
type bearerTransport struct{ key string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    copy := r.Clone(r.Context())
    copy.Header = r.Header.Clone()
    copy.Header.Set("Authorization", "Bearer " + t.key)
    return http.DefaultTransport.RoundTrip(copy)
}

// 需要 context、net/http、os、time 和 github.com/modelcontextprotocol/go-sdk/mcp。
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
client := mcp.NewClient(&mcp.Implementation{Name: "my-bot", Version: "1.0.0"}, nil)
session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
    Endpoint: os.Getenv("KEEPER_MCP_URL"),
    HTTPClient: &http.Client{Transport: bearerTransport{os.Getenv("MCP_API_KEY")}},
    DisableStandaloneSSE: true,
}, nil)
if err != nil { /* 向宿主报告连接失败，不输出密钥 */ return }
defer session.Close()
result, err := session.CallTool(ctx, &mcp.CallToolParams{
    Name: "get_key_usage", Arguments: map[string]any{},
})
// 检查 err 和 result.IsError，再把 result.Content 的文本交给 AI。
```

## 三个工具

| 工具 | 参数 | 返回内容 |
| --- | --- | --- |
| `get_quota_summary` | 可选 `pools` 字符串数组 | 各池套餐数量、5h/Weekly 的用量、比例、预估容量/剩余、最早重置和覆盖情况 |
| `get_key_usage` | 可选 `key_name` 或 `key_id`；可选 `range="24h"`、`granularity="hour"` | 指定 Key 的总览、模型分布、逐小时用量 |
| `get_model_tps` | 可选 `models` 数组；可选 `range="24h"`、`granularity="hour"` | 全站有效 Key 范围的模型速度、样本数、逐小时均值和变化范围 |

工具调用的 `name` 和 `arguments` 示例：

```json
{"name":"get_quota_summary","arguments":{}}
{"name":"get_quota_summary","arguments":{"pools":["codex","antigravity-gemini-models"]}}
{"name":"get_key_usage","arguments":{}}
{"name":"get_key_usage","arguments":{"key_name":"bot"}}
{"name":"get_key_usage","arguments":{"key_id":"42"}}
{"name":"get_model_tps","arguments":{}}
{"name":"get_model_tps","arguments":{"models":["gpt-5","gemini-3.8-flash"],"range":"24h","granularity":"hour"}}
```

`get_key_usage` 省略选择条件时精确匹配有效 Key 的别名 `bot`；`key_id` 是正整数的**字符串**。两者不可同时提供。名称不存在、已删除或重名会返回错误；重名时给出 ID 和脱敏候选，请再次显式选择。不会自动退化为全站查询，不返回完整 API Key。`get_model_tps` 不接受 Key 筛选条件。

`pools` 可选值为 `codex`、`antigravity-gemini-models`、`antigravity-claude-gpt-models`。当前聚合服务不包含 Argon 或特殊的单模型额度。空数组与省略筛选一样表示全部；`models` 按模型名匹配。额外参数和其他时间范围会被拒绝。

## 报告示例及口径

以下是虚构数据节选；实际响应包含所有匹配模型与完整小时序列，不只展示这些行。

```text
Key使用报告：bot（ID 42）
统计范围：2026-10-24 12:30:00 +08:00—2026-10-25 12:30:00 +08:00；严格最近24小时，结束时刻不包含在内。
时区：Asia/Shanghai；首尾不足一小时按实际边界显示。

总览
请求100次：成功98次，失败2次，成功率98.00%。
总Token：120000；输入：100000；输出：20000；推理：0。
缓存读取：80000；缓存写入：0；缓存读取率：80.00%。
Keeper测算费用：1.50 USD。

模型分布
模型 | 请求数 | Token | Token占比 | 费用USD | 平均TPS | 有效速度样本数
gpt-5 | 100 | 120000 | 100.00% | 1.50 | 70.00 | 80

逐小时用量
时间范围 | 请求 | 成功 | 失败 | 输入Token | 输出Token | 缓存读取Token | 缓存写入Token | 总Token | 费用USD
10-24 12:30:00 +08:00—10-24 13:00:00 +08:00 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0.00
```

小时范围严格为单次查询时刻的 `[now−24h, now)`，按 Keeper `TZ` 显示，保留无请求的零值小时。非整点通常有 25 行，整点通常有 24 行。桶沿用 Overview 的绝对整小时规则；夏令时重复的钟面小时用 UTC offset 区分。完整小时读取已有汇总，首尾读取窄范围事件，聚合水位之后的迟到/尚未聚合事件另行补齐；Key 解析、水位和所有数据在同一只读数据库快照内读取，小时总和与总览一致。不启动聚合、费用重算或上游刷新。

成功率为成功数/请求数，缓存读取率严格为 `cache_read_tokens / input_tokens`，分母为零时报告无法计算。Token 分项沿用原始字段，推理和缓存的包含关系不由 MCP 重算；总 Token 使用 Keeper 已存的 `total_tokens`。缺价时费用显示“无法完整计算”，不会把缺价视为免费。费用均为 Keeper 价格规则的测算值，不代表实际账单。

```text
全站模型TPS报告（不按Key筛选，沿用Keeper有效Key范围）
gpt-5
全时段平均：70.00 TPS；有效样本数：4。
小时均值变化范围：40.00—80.00 TPS。
最低小时：10-25 10:00:00 +08:00—10-25 11:00:00 +08:00，40.00 TPS。
最高小时：10-25 09:00:00 +08:00—10-25 10:00:00 +08:00，80.00 TPS。
时间范围 | 平均TPS | 有效速度样本数
10-25 09:00:00 +08:00—10-25 10:00:00 +08:00 | 80.00 | 3
10-25 10:00:00 +08:00—10-25 11:00:00 +08:00 | 40.00 | 1
10-25 11:00:00 +08:00—10-25 12:00:00 +08:00 | — | 0
```

TPS 是单请求 `输出 Token / 总耗时秒数` 的算术平均；输出和耗时必须均大于零，失败状态不额外排除速度样本。跨小时合并速度累计值和样本数，上例为 `(80×3+40×1)/4=70`，不是小时均值直接平均。变化范围仅指有样本小时的均值最低/最高，不是单请求极值；并列时报告最早发生的小时。`—` 表示无有效速度样本。

```text
Keeper聚合额度
Codex / GPT（codex） team ×3
- 5小时：已用30.00%，剩余70.00%。
  当前窗口已用：100000 Token、34.68 USD。
  预估总容量：300000 Token、80.00 USD。
  预估剩余：200000 Token、45.32 USD。
  用量覆盖2/3；估值覆盖3/3（直接1、历史1、同套餐补值1，补值样本1）；未知0。
```

额度直接来自现有聚合服务，百分比沿用 `remainingFraction`，不从金额反推。Token/USD 预估剩余分别为 `max(总容量估值−当前已用量,0)`，必要值缺失则无法估算。报告包含套餐数量、覆盖数量、缓存/过旧/缺失状态及最早重置倒计时。最早重置只代表最早一个凭据窗口；满额状态完全沿用服务端，不在 MCP 中自行把整池变满。5h 与 Weekly 同时生效，不能相加。部分覆盖时数值不代表整个池；查询时刻不等于上游数据已经同步完成。

## 错误处理

- HTTP `404`：入口关闭或路径错误。`401`：检查独立 MCP 密钥。
- 工具返回 `isError=true`、文本含 `costs_busy`：费用重算阻断期间稍后重试。初始化与工具发现仍可正常进行。
- `invalid_arguments` 或协议参数错误：修正参数。Key 不存在/重名：按报告提示明确选择，不要改为全站查询。
- `read_failed`：内部读取失败，稍后重试；不输出 SQL、完整 Key 或底层错误。
- 调用取消/断开连接会取消数据库读取并释放费用许可；宿主应设置超时。无请求、缺少速度样本和部分额度覆盖是正常报告内容，并非鉴权失败。
