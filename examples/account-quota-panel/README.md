# 聚合额度面板示例

Keeper 提供只读聚合结果，宿主/面板后端保存密钥并轮询，HTML 只负责显示。示例保留 Codex / GPT 与 Gemini Models 双列、5h/Weekly 双环，以及 standard、compact、mini 三种尺寸。前端由 Gemini 3.8 Flash 基于用户提供的 HTML 改造，独立于 Keeper 主界面。

## 1. 在 Keeper 开启 API

在 **Keeper 服务端**的 `.env` 中添加一个独立随机密钥（例如用 `openssl rand -hex 32` 生成）：

```dotenv
QUOTA_SUMMARY_API_KEY=替换为你自己的独立密钥
```

Compose 示例已使用 `env_file`，修改后重建 Keeper 容器，例如 `docker compose up -d --force-recreate cpa-usage-keeper`。留空关闭接口；更换后旧密钥立即在新容器中失效。它与 `LOGIN_PASSWORD`、`CPA_MANAGEMENT_KEY` 无关。

```http
GET https://keeper.example/keeper/api/v1/external/quota/summary
Authorization: Bearer <QUOTA_SUMMARY_API_KEY>
Accept: application/json
```

其中 `/keeper` 为可选的 `APP_BASE_PATH`。只支持 GET，不提供刷新或管理能力；浏览器会话不代替这个密钥，关闭网页登录也不会关闭 API 鉴权。不接受 query 参数中的密钥。不新增 CORS；由宿主请求，HTML 不直接跨域访问 Keeper。

| 状态码 | 含义 |
| --- | --- |
| 200 | 当前聚合快照 |
| 404 | 未设置密钥、路径不正确，或请求了不存在的外部操作 |
| 401 | 未携带有效的独立 Bearer 密钥 |
| 503 / `costs_busy` | 费用重算中，保留最后数据，稍后重试 |
| 500 | 读取失败，响应不包含内部错误细节 |

响应设置 `Cache-Control: no-store`。它直接复用管理界面的 `SummaryResponse`：

```text
generated_at, eligible, pending, groups[]
  group: provider, pool, label, plans[], stats, quota[]
  plans: plan, count
  stats: total_requests, success_count, failure_count,
         input_tokens, cache_read_tokens, total_tokens
  quota: window.seconds, remainingFraction,
         window_usage_tokens, window_usage_cost,
         window_usage_estimate_tokens, window_usage_estimate_cost,
         usage_state, estimate_state, coverage
  coverage: next_reset_at, oldest_snapshot_at, current, eligible, ...
```

缺失数值保持未知；数值 0 是真实零值。时间为完整 ISO-8601 字符串。`generated_at` 表示本次响应计算时间，数据本身的观察时间见 `coverage.oldest_snapshot_at`。接口不会刷新 CPA，也不改变 Keeper 配置的额度刷新频率。窗口到期后的满额/零当前用量沿用 Keeper 已有规则，等待真实成功刷新替换。

## 2. 启动宿主轮询

宿主需要 Node.js 20+。将以下变量放在宿主的私有环境中，**不要放在 HTML、URL、浏览器存储或对外提供静态文件的目录中**：

```dotenv
KEEPER_BASE_URL=https://keeper.example/keeper
QUOTA_SUMMARY_API_KEY=与Keeper服务端配置相同的独立密钥
```

可用 Node 的 env-file 支持运行示例（需要 Node 20.6+）：

```sh
node --env-file=/private/quota-panel.env examples/account-quota-panel/host-example.mjs
```

`host-example.mjs` 只往 stdout 输出 JSON lines，不输出密钥：

```text
{"type":"quota-status","data":{"state":"loading"}}
{"type":"quota-summary","data":{...原始SummaryResponse...}}
{"type":"quota-status","data":{"state":"ok","lastSuccessAt":"..."}}
```

宿主收到消息后，使用自身的安全 WebView/页面桥接机制调用：

```js
// 这两处调用发生在面板页面上下文。数据按 JSON 安全传递，不拼接未经序列化的代码。
window.updateAccountQuota(message.data)       // type === 'quota-summary'
window.updateAccountQuotaStatus(message.data) // type === 'quota-status'
```

若已有 Node 宿主，可直接导入 poller，替换示例的 stdout 桥接：

```js
import { createQuotaPoller } from './poller.mjs'

const poller = createQuotaPoller({
  baseUrl: process.env.KEEPER_BASE_URL, // Keeper 的部署根 URL，包含可选子路径
  apiKey: process.env.QUOTA_SUMMARY_API_KEY,
  onData: summary => deliverToPanel('quota-summary', summary),
  onStatus: status => deliverToPanel('quota-status', status),
})
void poller.start()
// 宿主关闭面板时调用 poller.stop()。
```

`deliverToPanel` 由你的宿主实现；示例不会部署新的转发服务器，也不会开放一个不鉴权的代理端点。

- 请求在完成后 60 秒发起下一次，10 秒超时，不重叠。
- 网络错误、非 2xx 或非法 JSON 按 60、120、240、300 秒退避；保留最后成功数据，成功后恢复 60 秒。
- `401` 报告 `unauthorized` 并停止；修正环境后重启宿主/poller。
- `stop()` 清理定时器并中止在途请求；状态为 `stopped`。
- 状态消息包括 `loading`、`ok`、`error`、`unauthorized`、`stopped`，以及可选的 `message`、`lastSuccessAt`。失败不把错误页或密钥传给 HTML。

## 3. 查看面板

通过宿主或本地 HTTP 服务提供本目录，默认进入真实数据模式，等待宿主注入。不要直接用 `file://` 打开模块脚本：

```sh
python3 -m http.server 4174 --bind 127.0.0.1 --directory examples/account-quota-panel
```

访问 `http://127.0.0.1:4174/`。`?mode=compact`、`?mode=mini` 选择初始尺寸，`?demo=1` 显式进入演示模式。原稿的 Tailwind CDN 和字体仍需要联网，首版不提供离线打包。

数据映射：

- 左列：`provider=codex`、`pool=codex`；右列：`provider=antigravity`、`pool=antigravity-gemini-models`。不把另一个 Claude/GPT 池相加。
- 5h/Weekly 依据 `window.seconds=18000/604800` 匹配，不依赖顺序。
- 圆环剩余比例为 `remainingFraction × 100`；缺失显示未知，0% 不画假的 2% 额度弧。
- 预估剩余 USD 为 `max(window_usage_estimate_cost - window_usage_cost, 0)`；任何必要值缺失均显示 `—`，真实 0 显示 `$0.00`。部分覆盖可在详情中查看，它仍是现有聚合口径的估计值。
- 套餐展示全部 `plans` 数量，包括 `×1`。
- 成功率为 `success_count / total_requests`；缓存率为 `cache_read_tokens / input_tokens`，与 Keeper 一致，分母为 0 时显示 `—`。
- 日期默认采用 `Asia/Shanghai`；倒计时来自 `coverage.next_reset_at` 并在本地更新、归零。只更新倒计时，不在客户端凭某个时间到期就把整个聚合组改成满额。
- 每次完整成功响应替换所有显示数据。缺少的组/窗口变为无数据；连接失败则保留上次成功结果并显示异常状态。

## 4. 验证

```sh
node --test examples/account-quota-panel/*.test.mjs
```

浏览器验证需要可用的 `agent-browser` CLI、浏览器及 CDN 网络访问。它检查三种尺寸、375px 窄屏内部边界、键盘操作、详情与连接状态隔离及缺组清空：

```sh
node --test examples/account-quota-panel/browser-verify.mjs
```

截图默认写入系统临时目录下的 `quota-demo-screenshots`；可用 `QUOTA_SCREENSHOT_DIR` 指定目录。

后端配置、密钥权限边界及费用读取协调测试：

```sh
go test ./internal/config/test ./internal/api/test ./internal/app/test
```
