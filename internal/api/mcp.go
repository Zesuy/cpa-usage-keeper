package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-usage-keeper/internal/botreports"
	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Stateless JSON responses cannot resume after the originating POST disconnects.
// Keep its context available even when the SDK detaches legacy protocol contexts.
type mcpHTTPContextKey struct{}

type mcpKeyUsageArgs struct {
	KeyName     *string `json:"key_name,omitempty"`
	KeyID       *string `json:"key_id,omitempty"`
	Range       string  `json:"range,omitempty"`
	Granularity string  `json:"granularity,omitempty"`
}
type mcpTPSArgs struct {
	Models      []string `json:"models,omitempty"`
	Range       string   `json:"range,omitempty"`
	Granularity string   `json:"granularity,omitempty"`
}
type mcpQuotaArgs struct {
	Pools []string `json:"pools,omitempty"`
}

type mcpRecoveryArgs struct {
	AfterID string `json:"after_id,omitempty"`
	Limit   *int   `json:"limit,omitempty"`
}

type quotaRecoveryReader interface {
	GetQuotaRecoveryEvents(context.Context, int64, int) (dto.QuotaRecoveryEventsPage, error)
}

func mcpText(s string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}, IsError: failed}
}

func mcpTool(name, description string, properties map[string]any) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description,
		InputSchema: map[string]any{"type": "object", "properties": properties, "additionalProperties": false},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: new(false)}}
}

func mcpRangeProperties() map[string]any {
	return map[string]any{
		"range":       map[string]any{"type": "string", "enum": []string{"24h"}, "description": "固定最近24小时，可省略"},
		"granularity": map[string]any{"type": "string", "enum": []string{"hour"}, "description": "固定按小时，可省略"},
	}
}

func registerMCPRoute(group *gin.RouterGroup, key string, usage service.UsageProvider, quotaProvider QuotaProvider, gate *service.CostReadGate) {
	key = strings.TrimSpace(key)
	expected := sha256.Sum256([]byte(key))
	server := mcp.NewServer(&mcp.Implementation{Name: "keeper-readonly", Version: "1.0.0"}, nil)
	reports, _ := usage.(service.BotUsageProvider)
	withRead := func(ctx context.Context, read func(context.Context) (string, error)) (*mcp.CallToolResult, any, error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		if requestCtx, ok := ctx.Value(mcpHTTPContextKey{}).(context.Context); ok {
			stop := context.AfterFunc(requestCtx, cancel)
			defer stop()
			if requestCtx.Err() != nil {
				cancel()
			}
		}
		release, ok := gate.Acquire()
		if !ok {
			return mcpText("costs_busy：费用重算中，请稍后重试。当前没有返回统计结果。", true), nil, nil
		}
		defer release()
		if err := ctx.Err(); err != nil {
			return mcpText("cancelled：查询已取消。", true), nil, nil
		}
		text, err := read(ctx)
		if err != nil {
			if errors.Is(err, repository.ErrQuotaRecoveryCursor) {
				return mcpText("invalid_arguments：事件游标超出当前数据库范围；恢复旧备份后请重新建立游标。", true), nil, nil
			}
			var keyErr *repository.BotKeyError
			if errors.As(err, &keyErr) {
				var b strings.Builder
				b.WriteString(keyErr.Error())
				for _, candidate := range keyErr.Candidates {
					fmt.Fprintf(&b, "\n候选：ID %d，名称%s，脱敏标识%s", candidate.ID, candidate.Label, candidate.MaskedKey)
				}
				return mcpText(b.String(), true), nil, nil
			}
			if ctx.Err() != nil {
				return mcpText("cancelled：查询已取消。", true), nil, nil
			}
			// Neither database errors nor request/credential values are sent to the AI.
			return mcpText("read_failed：读取Keeper统计失败，请稍后重试。", true), nil, nil
		}
		return mcpText(text, false), nil, nil
	}
	keyProperties := mcpRangeProperties()
	keyProperties["key_name"] = map[string]any{"type": "string", "minLength": 1, "description": "有效Key的精确别名，默认bot；不能与key_id同时提供"}
	keyProperties["key_id"] = map[string]any{"type": "string", "pattern": "^[1-9][0-9]*$", "description": "重名时使用的Key ID字符串；不能与key_name同时提供"}
	mcp.AddTool(server, mcpTool("get_key_usage", "读取指定Key最近24小时的总用量、模型分布（含平均TPS）及完整逐小时请求/Token/费用报告。默认查名称bot，不会退化为全站查询。不刷新上游。", keyProperties),
		func(ctx context.Context, _ *mcp.CallToolRequest, args mcpKeyUsageArgs) (*mcp.CallToolResult, any, error) {
			if args.KeyName != nil && args.KeyID != nil {
				return mcpText("invalid_arguments：key_name与key_id不能同时提供。", true), nil, nil
			}
			query := repository.BotUsageQuery{KeyScoped: true, KeyName: "bot"}
			if args.KeyName != nil {
				query.KeyName = strings.TrimSpace(*args.KeyName)
				if query.KeyName == "" {
					return mcpText("invalid_arguments：key_name不能为空。", true), nil, nil
				}
			}
			if args.KeyID != nil {
				id, err := strconv.ParseInt(*args.KeyID, 10, 64)
				if err != nil || id <= 0 {
					return mcpText("invalid_arguments：key_id必须是有效的正整数ID。", true), nil, nil
				}
				query.KeyID = id
				query.KeyName = ""
			}
			return withRead(ctx, func(ctx context.Context) (string, error) {
				if reports == nil {
					return "", errors.New("missing report provider")
				}
				r, err := reports.GetBotUsage(ctx, query, time.Now())
				if err != nil {
					return "", err
				}
				if r == nil {
					return "", errors.New("missing report")
				}
				return botreports.KeyUsage(r), nil
			})
		})
	tpsProperties := mcpRangeProperties()
	tpsProperties["models"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "description": "可选模型名称列表；省略或空数组表示全部模型"}
	mcp.AddTool(server, mcpTool("get_model_tps", "读取全站有效Key范围内各模型最近24小时的逐小时平均TPS、有效样本数和小时均值变化范围。不按Key筛选；范围不是单请求极值。", tpsProperties),
		func(ctx context.Context, _ *mcp.CallToolRequest, args mcpTPSArgs) (*mcp.CallToolResult, any, error) {
			models, err := mcpNames(args.Models)
			if err != nil {
				return mcpText(err.Error(), true), nil, nil
			}
			return withRead(ctx, func(ctx context.Context) (string, error) {
				if reports == nil {
					return "", errors.New("missing report provider")
				}
				r, err := reports.GetBotUsage(ctx, repository.BotUsageQuery{Models: models}, time.Now())
				if err != nil {
					return "", err
				}
				if r == nil {
					return "", errors.New("missing report")
				}
				return botreports.ModelTPS(r, models), nil
			})
		})
	poolIDs := []string{"codex", "antigravity-gemini-models", "antigravity-claude-gpt-models"}
	mcp.AddTool(server, mcpTool("get_quota_summary", "查看当前全部有效供应侧凭据池的聚合额度：5小时/周已用与剩余比例、预估剩余Token/USD、最近重置及覆盖情况；Codex还报告缓存的RST重置卡可用数量及覆盖数。这是凭据池额度，不属于某个客户端Key。只读现有结果，不触发刷新；卡数量变化不代表使用。", map[string]any{
		"pools": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": poolIDs}, "description": "可选模型池列表；省略或空数组表示全部模型池"},
	}), func(ctx context.Context, _ *mcp.CallToolRequest, args mcpQuotaArgs) (*mcp.CallToolResult, any, error) {
		return withRead(ctx, func(ctx context.Context) (string, error) {
			provider, ok := quotaProvider.(quotaSummaryReader)
			if !ok {
				return "", errors.New("missing summary provider")
			}
			now := time.Now()
			summary, err := provider.GetQuotaSummary(ctx)
			if err != nil {
				return "", err
			}
			if len(args.Pools) > 0 {
				wanted := map[string]bool{}
				for _, pool := range args.Pools {
					wanted[pool] = true
				}
				groups := make([]quota.SummaryGroup, 0, len(summary.Groups))
				for _, g := range summary.Groups {
					if wanted[g.Pool] {
						groups = append(groups, g)
					}
				}
				summary.Groups = groups
			}
			return botreports.Quota(summary, now), nil
		})
	})
	recoveryTool := mcpTool("get_quota_recovery_events", "按ID读取已持久化的多账号Weekly非预期恢复事件。两次官方观测形成单账号命中，30分钟内三个不同账号才记录。返回中文及structuredContent；不是官方补偿的成因认定，无法排除外部用卡。只读，不刷新上游。", map[string]any{
		"after_id": map[string]any{"type": "string", "pattern": "^(0|[1-9][0-9]*)$", "description": "返回ID大于此游标的事件，默认0；保存next_after_id供下次查询"},
		"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "每页数量，默认50"},
	})
	recoveryTool.OutputSchema = map[string]any{
		"type": "object", "required": []string{"events", "next_after_id", "has_more", "detector_started_at"},
		"properties": map[string]any{
			"events":        map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"next_after_id": map[string]any{"type": "string"}, "has_more": map[string]any{"type": "boolean"},
			"detector_started_at": map[string]any{"type": "string"},
		},
	}
	mcp.AddTool(server, recoveryTool, func(ctx context.Context, _ *mcp.CallToolRequest, args mcpRecoveryArgs) (*mcp.CallToolResult, any, error) {
		afterID, limit := int64(0), 50
		if args.AfterID != "" {
			var err error
			afterID, err = strconv.ParseInt(args.AfterID, 10, 64)
			if err != nil || afterID < 0 {
				return mcpText("invalid_arguments：after_id必须是有效的非负整数ID字符串。", true), nil, nil
			}
		}
		if args.Limit != nil {
			if *args.Limit < 1 || *args.Limit > 200 {
				return mcpText("invalid_arguments：limit必须在1到200之间。", true), nil, nil
			}
			limit = *args.Limit
		}
		var page dto.QuotaRecoveryEventsPage
		result, _, err := withRead(ctx, func(ctx context.Context) (string, error) {
			provider, ok := quotaProvider.(quotaRecoveryReader)
			if !ok {
				return "", errors.New("missing quota recovery provider")
			}
			var readErr error
			page, readErr = provider.GetQuotaRecoveryEvents(ctx, afterID, limit)
			if readErr != nil {
				return "", readErr
			}
			return botreports.QuotaRecoveryEvents(page), nil
		})
		if err != nil || result.IsError {
			return result, nil, err
		}
		return result, page, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 128 * 1024, PropagateRequestCancellation: true})
	group.Any("/mcp", func(c *gin.Context) {
		setNoStoreHeaders(c)
		if key == "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		fields := strings.Fields(c.GetHeader("Authorization"))
		valid := len(c.Request.Header.Values("Authorization")) == 1 && len(fields) == 2 && strings.EqualFold(fields[0], "Bearer")
		if valid {
			actual := sha256.Sum256([]byte(fields[1]))
			valid = subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
		}
		if !valid {
			c.Header("WWW-Authenticate", `Bearer realm="keeper-mcp"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		request := c.Request.WithContext(context.WithValue(c.Request.Context(), mcpHTTPContextKey{}, c.Request.Context()))
		handler.ServeHTTP(mcpNoStoreWriter{c.Writer}, request)
	})
}

func mcpNames(names []string) ([]string, error) {
	var result []string
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("invalid_arguments：模型名称不能为空。")
		}
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result, nil
}

// The SDK may replace cache headers when encoding JSON; enforce no-store at write time.
type mcpNoStoreWriter struct{ http.ResponseWriter }

func (w mcpNoStoreWriter) WriteHeader(code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(code)
}
func (w mcpNoStoreWriter) Write(p []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(p)
}
func (w mcpNoStoreWriter) Flush() {
	w.Header().Set("Cache-Control", "no-store")
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w mcpNoStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
