// main.go
// token-comper-demo 入口：HTTP 服务，把所有压缩器串成流水线。
//
// 运行：go run .
// 接口：
//   POST /compress  接收 OpenAI 格式请求体，走完整压缩流水线
//   GET  /stats     查看计量统计（各引擎节省）
//
// 流水线（按 Input.Role 分派）：
//   system    → PromptCompressor（Caveman 规则压缩）→ ToolPruner（工具裁剪）
//   user(热区) → MultimodalDetailDowngrader（图片降级）
//   tool(热区) → RTKCompressor（按类型压缩）→ SessionDedup（会话去重）→ Guard（护栏）
//   assistant → 原样（不压缩用户/助手主动内容）

package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"token-comper-demo/logger"
	"token-comper-demo/middleware"
	"token-comper-demo/service/compression"
	"token-comper-demo/upstream"
)

//go:embed web/index.html
var webFS embed.FS

// 全局实例：压缩器 + 计量器
var (
	promptC    = compression.NewPromptCompressor()
	toolP      = compression.NewToolPruner()
	multimodal = compression.NewMultimodalDetailDowngrader()
	rtk        = compression.NewRTKCompressor()
	sessionD   = compression.NewSessionDedup()
	partitioner = compression.NewPartitioner(true) // 前缀对齐 + 注入 cache_control 断点
	meter      = compression.NewMeter()

	// 结构化/历史区压缩器（D8 / F11 / H14）
	gcfC       = compression.NewGCFCompressor()
	ccrStore   = compression.NewCCRStore()
	ccrC       = compression.NewCCRCompressor(ccrStore)
	relevanceC = compression.NewRelevanceCompressor()

	// 无损格式清理（作用于 user 消息）
	normalizeC = compression.NewNormalizeCompressor()

	// LLMLingua 近似（S4 高风险有损）+ OmniGlyph（文本转图片）
	lossyC     = compression.NewLossyCompressor()
	omniglyphC = compression.NewOmniGlyphCompressor()

	// 历史压缩原子引擎（aggressive / ultra 复合预设用到）
	summarizeC = compression.NewSummarizer()
	ageC       = compression.NewAger()
	// 抽取式压缩属可信有损，仅做 fail-closed + 幂等兜底（不严卡保真率，否则会拒绝正当摘要）
	summarizeGuard = compression.NewGuard(compression.GuardConfig{
		Enabled:          true,
		EnableIdempotent: true,
		Fidelity: compression.FidelityConfig{
			Enabled:      false, // 摘要天然大幅缩减，关掉 token 存活率硬性闸门
			MinJsonKeyPct: 90,
		},
	}, summarizeC)
	ageGuard = compression.NewGuard(compression.GuardConfig{
		Enabled:          true,
		EnableIdempotent: true,
		Fidelity: compression.FidelityConfig{
			Enabled:      false,
			MinJsonKeyPct: 90,
		},
	}, ageC)

	// LLMLingua 高风险有损，包护栏（fidelity gate 兜底：代码块/数字/JSON key 存活校验）
	lossyGuard = compression.NewGuard(compression.GuardConfig{
		Enabled:          true,
		EnableIdempotent: true,
		Fidelity: compression.FidelityConfig{
			Enabled:             true,
			MinTokenSurvivalPct: 95,
			MinJsonKeyPct:       90,
		},
	}, lossyC)

	// RTK 有损，包护栏（fail-closed + 幂等 + 保真校验）
	rtkGuard = compression.NewGuard(compression.GuardConfig{
		Enabled:          true,
		EnableIdempotent: true,
		Fidelity: compression.FidelityConfig{
			Enabled:               true,
			MinTokenSurvivalPct:   95,
			MinJsonKeyPct:         90,
			CheckNumericIntegrity: false, // 默认关，避免误伤「删 INFO 行」这类合理压缩
		},
	}, rtk)

	// 上游模型客户端（火山方舟）
	upClient = &upstream.Client{}

	// API key 管理（内存版）
	keyManager = NewKeyManager()

	// Compression Studio 最新级联快照（供 GET /compression/studio 实时拉取）
	studioMu       sync.Mutex
	lastStudioTrace []EngineTraceEntry
	lastStudioAt    string

	// 请求 ID 原子计数器（计量/审计用）
	reqIDCounter uint64
)

// OpenAI 请求体（最小子集，够 demo 用）
type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []chatMessage    `json:"messages"`
	Tools       []map[string]any `json:"tools,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`                    // string 或多模态数组
	ToolCallID string           `json:"tool_call_id,omitempty"`     // tool 消息回填结果用，关联 assistant 的 tool_calls
	ToolCalls  []map[string]any `json:"tool_calls,omitempty"`       // assistant 消息的工具调用请求
}

// 压缩结果响应
type compressResponse struct {
	Original      string                     `json:"original"`
	Messages      []map[string]any           `json:"messages"`
	Tools         []map[string]any           `json:"tools,omitempty"` // 裁剪后的工具定义
	Steps         []string                   `json:"steps"`           // 每步做了什么
	Saved         map[string]int             `json:"saved"`           // 各引擎节省
	Total         int                        `json:"total"`           // 总计节省
	CacheHit      bool                       `json:"cache_hit"`
	Zones         map[string]int             `json:"zones"`           // 前缀对齐分区统计
	Upstream      *upstream.ChatCompletionResponse `json:"upstream,omitempty"` // 上游真实响应
	UpstreamError string                     `json:"upstream_error,omitempty"`
	EngineTrace   []EngineTraceEntry         `json:"engine_trace,omitempty"` // 本次请求引擎级联（Compression Studio 用）
}

// EngineTraceEntry 单次压缩引擎调用记录（实时级联可视化用）
type EngineTraceEntry struct {
	Engine      string   `json:"engine"`
	Fired       bool     `json:"fired"`
	SavedTokens int      `json:"saved_tokens"`
	Note        string   `json:"note,omitempty"`
	Techniques  []string `json:"techniques,omitempty"`
	Rules       []string `json:"rules,omitempty"`
	Confidence  float64  `json:"confidence,omitempty"`
}

func main() {
	// 加载 .env（含上游配置，key 不提交 git）
	loadEnv(".env")
	// 初始化日志脱敏（写入前打码 PII/凭据）
	logger.InitRedactor()
	// 初始化上游客户端（火山方舟）
	if c, err := upstream.FromEnv(); err == nil {
		upClient = c
		logger.Info("上游已配置: " + c.BaseURL)
	} else {
		logger.Info("⚠️ 上游未配置: " + err.Error())
	}

	// 初始化精确缓存存储（不初始化会 panic）
	middleware.InitCacheStore(middleware.NewMemoryCache())

	r := gin.Default()

	// 中间件（流水线外层：整体判断）
	r.Use(middleware.OutputControl())
	r.Use(middleware.ExactCache())

	r.POST("/compress", handleCompress)
	r.POST("/test/engine", handleTestEngine) // 单组件测试：指定引擎单独跑
	r.GET("/stats", handleStats)
	r.GET("/compression/studio", handleStudio) // Compression Studio 实时级联快照
	r.GET("/admin/policy", handleGetPolicy)
	r.POST("/admin/policy", handleSetPolicy)
	r.POST("/admin/keys", handleCreateKey)
	r.GET("/admin/keys", handleListKeys)
	r.PUT("/admin/keys/:key", handleUpdateKey)
	r.DELETE("/admin/keys/:key", handleDeleteKey)
	r.GET("/admin/exclusions", handleListExclusions)
	r.POST("/admin/exclusions", handleCreateExclusion)
	r.DELETE("/admin/exclusions/:id", handleDeleteExclusion)
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/", handleHome)

	logger.Info("token-comper-demo 启动: http://localhost:4040")
	// 端口可用 PORT 环境变量覆盖（避免冲突）
	port := os.Getenv("PORT")
	if port == "" {
		port = "4040"
	}
	if err := r.Run(":" + port); err != nil {
		logger.Error("服务启动失败: " + err.Error())
	}
}

// handleCompress 处理压缩请求（插件化流水线 + 真实调用上游）
// 顺序：
//   1. 解析用户 compression 字段 → resolvePlugins 得最终插件列表
//   2. 前缀对齐分区（prefix 插件）
//   3. 按插件逐条消息压缩
//   4. 真实调用上游模型（火山方舟）
//   5. 返回：上游真实回答 + 压缩节省统计
func handleCompress(c *gin.Context) {
	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid body: " + err.Error()})
		return
	}

	reqID := newRequestID()
	var steps []string
	savedByEngine := map[string]int{}
	zones := map[string]int{}
	engineTrace := []EngineTraceEntry{} // 本次请求引擎级联（Compression Studio 用）

	// ========== 1. 插件解析：从 Authorization header 读 API key → 查压缩策略 ==========
	// 有 key 且配了策略 → 用 key 的策略（受管理员上限钳制）
	// 无 key / 未配置 → 管理员默认策略
	var keyComp *KeyCompression
	if k := extractBearer(c.GetHeader("Authorization")); k != "" {
		if rec, ok := keyManager.Get(k); ok {
			keyComp = &rec.Compression
		}
	}
	policy := getCurrentPolicy()
	plugins := resolvePlugins(keyComp, policy)
	intensity := compression.IntensityFromString(policy.IntensityCeil) // 默认用管理员上限
	if keyComp != nil && keyComp.Intensity != "" {
		intensity = clampIntensity(keyComp.Intensity, policy.IntensityCeil)
	}
	if keyComp != nil {
		stepLog("key 压缩策略", pluginsNames(plugins), &steps)
	} else {
		stepLog("默认策略（无 key）", pluginsNames(plugins), &steps)
	}

	// ========== 1.5 排除规则（per-model / per-endpoint）==========
	if ex := matchExclusion(req.Model, upClient.BaseURL); ex != nil {
		plugins, _ = applyExclusions(plugins, req.Model, upClient.BaseURL)
		if ex.Action == "skip" {
			steps = append(steps, "命中排除规则 #"+ex.ID+" ("+ex.Type+"="+ex.Pattern+") → 跳过全部压缩")
		} else {
			steps = append(steps, "命中排除规则 #"+ex.ID+" ("+ex.Type+"="+ex.Pattern+") → 仅无损压缩")
		}
	}

	// ========== 2. 前缀对齐（prefix 插件）==========
	if hasPlugin(plugins, "prefix") {
		partitioned := partitioner.Partition(chatMessagesToMaps(req.Messages))
		zones["frozen"] = len(partitioned.Frozen)
		zones["archived"] = len(partitioned.Archived)
		zones["hot"] = len(partitioned.Hot)
		steps = append(steps, fmt.Sprintf("prefix-align 分区 frozen=%d archived=%d hot=%d", len(partitioned.Frozen), len(partitioned.Archived), len(partitioned.Hot)))
	}

	// ========== 3. 工具裁剪（tool-prune 插件，整组处理）==========
	toolsResult := req.Tools
	if hasPlugin(plugins, "tool-prune") && len(req.Tools) > 0 {
		toolDefs := toolsToDefs(req.Tools)
		toolIn := &compression.Input{Role: compression.RoleSystem, Region: compression.RegionFrozen, TaskType: detectTaskType(req), Content: toolDefs, CallCtx: &compression.CallContext{RequestID: reqID}}
		if toolP.IsApplicable(toolIn) {
			toolOut := applyWithMeter(toolP, toolIn, savedByEngine, &steps, meter, &engineTrace)
			if toolOut != nil && toolOut.Content != nil {
				if defs, ok := toolOut.Content.([]compression.ToolDef); ok {
					toolsResult = defsToMaps(defs)
				}
			}
		}
	}

	// ========== 4. 会话去重（session 插件，tool 消息整组）==========
	if hasPlugin(plugins, "session") {
		var toolContents []string
		var toolIndexes []int
		for i, msg := range req.Messages {
			if compression.Role(msg.Role) == compression.RoleTool {
				if s, ok := msg.Content.(string); ok {
					toolContents = append(toolContents, s)
					toolIndexes = append(toolIndexes, i)
				}
			}
		}
		if len(toolContents) > 1 {
			dedupIn := &compression.Input{Role: compression.RoleTool, Region: compression.RegionHot, Content: toolContents, CallCtx: &compression.CallContext{RequestID: reqID}}
			dedupOut := applyWithMeter(sessionD, dedupIn, savedByEngine, &steps, meter, &engineTrace)
			if dedupOut != nil && dedupOut.Content != nil {
				if deduped, ok := dedupOut.Content.([]string); ok && len(deduped) == len(toolIndexes) {
					for j, idx := range toolIndexes {
						req.Messages[idx].Content = deduped[j]
					}
				}
			}
		}
	}

	// ========== 4.5 历史区整组处理：relevance（按最后查询过滤不相关句）==========
	hotStart := lastUserIndex(req.Messages)
	lastQuery := lastUserQuery(req)
	if hasPlugin(plugins, "relevance") {
		var archivedIdx []int
		var archivedContents []string
		for i := 0; i < hotStart; i++ {
			if compression.Role(req.Messages[i].Role) == compression.RoleSystem {
				continue
			}
			if s, ok := req.Messages[i].Content.(string); ok {
				archivedIdx = append(archivedIdx, i)
				archivedContents = append(archivedContents, s)
			}
		}
		if len(archivedContents) > 1 && lastQuery != "" {
			relIn := &compression.Input{Region: compression.RegionArchived, Role: compression.RoleUser, Content: archivedContents, Query: lastQuery, CallCtx: &compression.CallContext{RequestID: reqID}}
			relOut := applyWithMeter(relevanceC, relIn, savedByEngine, &steps, meter, &engineTrace)
			if relOut != nil {
				if filtered, ok := relOut.Content.([]string); ok && len(filtered) == len(archivedIdx) {
					for j, idx := range archivedIdx {
						req.Messages[idx].Content = filtered[j]
					}
				}
			}
		}
	}

	// ========== 5. 逐条消息过插件 ==========
	// 从 assistant 的 tool_calls 解析 shell 命令（供 RTK 命令感知压缩）
	toolCommands := extractToolCommands(req.Messages)

	var outMessages []map[string]any
	for i, msg := range req.Messages {
		// Region 接线：system→冻结区，最后一条 user 之后→热区，之前→封存区
		region := compression.RegionArchived
		if compression.Role(msg.Role) == compression.RoleSystem {
			region = compression.RegionFrozen
		} else if i >= hotStart {
			region = compression.RegionHot
		}

		in := &compression.Input{
			Region:     region,
			Role:       compression.Role(msg.Role),
			Content:    msg.Content,
			TaskType:   detectTaskType(req),
			AllowLossy: true,
			Query:      lastQuery,
			Command:    toolCommands[msg.ToolCallID],
			CallCtx:    &compression.CallContext{RequestID: reqID},
		}

		var out *compression.Output

		switch compression.Role(msg.Role) {
		case compression.RoleSystem:
			if hasPlugin(plugins, "caveman") {
				out = runPlugin(PluginMeta{Name: "caveman"}, in, intensity, savedByEngine, &steps, &engineTrace)
			}
		case compression.RoleUser:
			// 无损格式清理（合并空行/空格/重复标点），作用于纯文本 user 消息
			if hasPlugin(plugins, "normalize") {
				out = runPlugin(PluginMeta{Name: "normalize"}, in, intensity, savedByEngine, &steps, &engineTrace)
			}
			// 多模态图片降级（仅多模态数组消息，与 normalize 互斥）
			if out == nil && hasPlugin(plugins, "multimodal") {
				out = runPlugin(PluginMeta{Name: "multimodal"}, in, intensity, savedByEngine, &steps, &engineTrace)
			}
		case compression.RoleTool:
			// 管道堆叠：rtk + gcf 同时启用时串联，否则单个
			useRtk := hasPlugin(plugins, "rtk")
			useGcf := hasPlugin(plugins, "gcf")
			switch {
			case useRtk && useGcf:
				pipe := compression.NewPipeline("rtk-gcf", rtkGuard, gcfC)
				out = applyWithMeter(pipe, in, savedByEngine, &steps, meter, &engineTrace)
			case useRtk:
				out = runPlugin(PluginMeta{Name: "rtk"}, in, intensity, savedByEngine, &steps, &engineTrace)
			case useGcf:
				out = runPlugin(PluginMeta{Name: "gcf"}, in, intensity, savedByEngine, &steps, &engineTrace)
			}
		default:
			// assistant：原样（历史区超长内容走下方 ccr）
		}

		// 历史区超长消息归档（ccr，任何角色，含 assistant）
		if out == nil && hasPlugin(plugins, "ccr") && region == compression.RegionArchived {
			if s, ok := msg.Content.(string); ok && len([]rune(s)) >= 2000 {
				ccrIn := &compression.Input{Region: compression.RegionArchived, Role: compression.Role(msg.Role), Content: s, CallCtx: &compression.CallContext{RequestID: reqID}}
				out = applyWithMeter(ccrC, ccrIn, savedByEngine, &steps, meter, &engineTrace)
			}
		}

		// 历史区长内容：抽取式摘要 + 渐进老化（aggressive / ultra 引入）
		// 在 ccr 之后、其它处理之上叠加，进一步压缩陈旧历史
		if region == compression.RegionArchived {
			histContent := contentOf(out, msg.Content)
			if s, ok := histContent.(string); ok && len([]rune(s)) >= 300 {
				if hasPlugin(plugins, "summarize") {
					sumIn := &compression.Input{Region: region, Role: compression.Role(msg.Role), Content: s, Query: lastQuery, AllowLossy: true, CallCtx: &compression.CallContext{RequestID: reqID}}
					if so := applyWithMeter(summarizeGuard, sumIn, savedByEngine, &steps, meter, &engineTrace); so != nil && so.Content != nil {
						s = contentString(so.Content)
						out = so
					}
				}
				if hasPlugin(plugins, "age") {
					ageIn := &compression.Input{Region: region, Role: compression.Role(msg.Role), Content: s, AllowLossy: true, CallCtx: &compression.CallContext{RequestID: reqID}}
					if ao := applyWithMeter(ageGuard, ageIn, savedByEngine, &steps, meter, &engineTrace); ao != nil && ao.Content != nil {
						out = ao
					}
				}
			}
		}

		m := map[string]any{
			"role":    msg.Role,
			"content": contentOf(out, msg.Content),
		}
		// 透传工具调用协议字段（assistant 的 tool_calls / tool 的 tool_call_id）
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			m["tool_calls"] = msg.ToolCalls
		}
		outMessages = append(outMessages, m)
	}

	// ========== 6. 真实调用上游模型 ==========
	upstreamReq := upstream.ChatCompletionRequest{
		Model:       req.Model,
		Messages:    mapsToUpstreamMessages(outMessages),
		Tools:       toolsResult,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	chatResp, err := upClient.Chat(c.Request.Context(), upstreamReq)

	// 组装响应（不管上游成败，都返回压缩统计）
	total := 0
	for _, v := range savedByEngine {
		total += v
	}

	resp := compressResponse{
		Original:    mustJSON(req),
		Messages:    outMessages,
		Tools:       toolsResult,
		Steps:       steps,
		Saved:       savedByEngine,
		Total:       total,
		Zones:       zones,
		EngineTrace: engineTrace,
	}

	// 更新 Compression Studio 最新级联快照
	studioMu.Lock()
	lastStudioTrace = engineTrace
	lastStudioAt = nowStr()
	studioMu.Unlock()
	if err != nil {
		resp.UpstreamError = err.Error()
		logger.Error("上游调用失败: " + err.Error())
	} else if chatResp != nil {
		resp.Upstream = chatResp
	}

	c.JSON(http.StatusOK, resp)
}

// stepLog 记录插件选择步骤
func stepLog(label string, vals []string, steps *[]string) {
	*steps = append(*steps, label+": "+joinStrings(vals))
}

// joinStrings 把 []string 拼成逗号分隔
func joinStrings(vals []string) string {
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

// pluginsNames 取插件名列表
func pluginsNames(plugins []PluginMeta) []string {
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, p.Name)
	}
	return names
}

// hasPlugin 判断插件列表里是否有指定名字
func hasPlugin(plugins []PluginMeta, name string) bool {
	for _, p := range plugins {
		if p.Name == name {
			return true
		}
	}
	return false
}

// mapsToUpstreamMessages 把压缩后的 messages 转成上游请求格式
func mapsToUpstreamMessages(msgs []map[string]any) []upstream.Message {
	out := make([]upstream.Message, 0, len(msgs))
	for _, m := range msgs {
		role, _ := m["role"].(string)
		msg := upstream.Message{Role: role, Content: m["content"]}
		if tc, ok := m["tool_call_id"].(string); ok {
			msg.ToolCallID = tc
		}
		if tcs, ok := m["tool_calls"].([]map[string]any); ok {
			msg.ToolCalls = tcs
		}
		out = append(out, msg)
	}
	return out
}

// toolsToDefs 把 []map[string]any 转成 []ToolDef
func toolsToDefs(tools []map[string]any) []compression.ToolDef {
	defs := make([]compression.ToolDef, 0, len(tools))
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		typ, _ := t["type"].(string)
		defs = append(defs, compression.ToolDef{
			Type: typ,
			Function: compression.ToolFunction{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	return defs
}

// defsToMaps 把 []ToolDef 转回 []map[string]any
func defsToMaps(defs []compression.ToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"type": d.Type,
			"function": map[string]any{
				"name":        d.Function.Name,
				"description": d.Function.Description,
				"parameters":  d.Function.Parameters,
			},
		})
	}
	return out
}

// chatMessagesToMaps 把 []chatMessage 转成 []map[string]any（供 Partitioner 使用）
func chatMessagesToMaps(msgs []chatMessage) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{"role": m.Role, "content": m.Content})
	}
	return out
}

// applyWithMeter 执行一个压缩器并记录计量；trace 非 nil 时追加一条级联记录
func applyWithMeter(cp compression.Compressor, in *compression.Input, saved map[string]int, steps *[]string, meter *compression.Meter, trace *[]EngineTraceEntry) *compression.Output {
	if cp == nil {
		return nil
	}
	if !cp.IsApplicable(in) {
		if trace != nil {
			*trace = append(*trace, EngineTraceEntry{Engine: cp.Name(), Fired: false})
		}
		return nil
	}
	out, err := cp.Apply(context.Background(), in)
	if err != nil {
		*steps = append(*steps, cp.Name()+" 失败(fail-closed): "+err.Error())
		if trace != nil {
			*trace = append(*trace, EngineTraceEntry{Engine: cp.Name(), Fired: false, Note: err.Error()})
		}
		return nil
	}
	if out != nil && out.SavedTokens > 0 {
		*steps = append(*steps, cp.Name()+" 省 "+itoa(out.SavedTokens)+" token")
		saved[cp.Name()] += out.SavedTokens
		reqID := "unknown"
		if in != nil && in.CallCtx != nil && in.CallCtx.RequestID != "" {
			reqID = in.CallCtx.RequestID
		}
		meter.Record(compression.MeterEntry{
			RequestID:        reqID,
			EngineName:       cp.Name(),
			OriginalTokens:   out.Metrics.OriginalTokens,
			CompressedTokens: out.Metrics.CompressedTokens,
			SavedTokens:      out.SavedTokens,
		})
		if trace != nil {
			*trace = append(*trace, EngineTraceEntry{
				Engine:      cp.Name(),
				Fired:       true,
				SavedTokens: out.SavedTokens,
				Techniques:  out.Techniques,
				Rules:       out.Rules,
				Confidence:  out.Confidence,
			})
		}
		return out
	}
	if trace != nil {
		*trace = append(*trace, EngineTraceEntry{Engine: cp.Name(), Fired: out != nil, SavedTokens: 0})
	}
	return out
}

// handleStats 查看计量统计
func handleStats(c *gin.Context) {
	s := meter.Stats()
	c.JSON(200, gin.H{
		"total_saved": s.TotalSaved,
		"count":       s.TotalEntries,
		"by_engine":   s.EngineSaved,
		"cache_hits":  s.CacheHits,
	})
}

// handleStudio 返回最近一次压缩的引擎级联快照 + 实时累计统计（Compression Studio 面板轮询）
func handleStudio(c *gin.Context) {
	studioMu.Lock()
	trace := lastStudioTrace
	at := lastStudioAt
	studioMu.Unlock()

	s := meter.Stats()
	c.JSON(200, gin.H{
		"last_trace": trace,
		"last_at":    at,
		"aggregate": gin.H{
			"total_saved": s.TotalSaved,
			"count":       s.TotalEntries,
			"by_engine":   s.EngineSaved,
		},
	})
}

// handleGetPolicy 管理员：读取当前压缩策略
func handleGetPolicy(c *gin.Context) {
	p := getCurrentPolicy()
	c.JSON(200, gin.H{
		"max_lossy":       p.MaxLossy,
		"default_plugins": p.DefaultPlugins,
		"intensity_ceil":  p.IntensityCeil,
		"plugins":         pluginRegistry,
	})
}

// handleSetPolicy 管理员：更新压缩策略（存内存）
func handleSetPolicy(c *gin.Context) {
	var p AdminPolicy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(400, gin.H{"error": "invalid policy: " + err.Error()})
		return
	}
	setCurrentPolicy(p)
	c.JSON(200, gin.H{"ok": true, "policy": getCurrentPolicy()})
}

// handleCreateKey 管理员：生成新 API key（绑定压缩策略）
func handleCreateKey(c *gin.Context) {
	var req struct {
		Name   string         `json:"name"`
		Config KeyCompression `json:"compression"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	rec, err := keyManager.Create(req.Name, req.Config)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, rec)
}

// handleListKeys 管理员：列出所有 key
func handleListKeys(c *gin.Context) {
	c.JSON(200, gin.H{"keys": keyManager.List()})
}

// handleUpdateKey 管理员：更新 key 的压缩策略
func handleUpdateKey(c *gin.Context) {
	key := c.Param("key")
	var req struct {
		Config KeyCompression `json:"compression"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if !keyManager.Update(key, req.Config) {
		c.JSON(404, gin.H{"error": "key not found"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// handleDeleteKey 管理员：删除 key
func handleDeleteKey(c *gin.Context) {
	key := c.Param("key")
	keyManager.Delete(key)
	c.JSON(200, gin.H{"ok": true})
}

// detectTaskType 简化：有 tools 就是 agent
func detectTaskType(req chatRequest) string {
	if len(req.Tools) > 0 {
		return "agent"
	}
	return "chat"
}

// lastUserIndex 返回最后一条 user 消息的下标（无 user 返回 0）
func lastUserIndex(msgs []chatMessage) int {
	idx := 0
	for i, m := range msgs {
		if compression.Role(m.Role) == compression.RoleUser {
			idx = i
		}
	}
	return idx
}

// lastUserQuery 返回最后一条 user 消息的文本内容（供 Relevance 做相关性评分）
func lastUserQuery(req chatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if compression.Role(req.Messages[i].Role) == compression.RoleUser {
			if s, ok := req.Messages[i].Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

// extractToolCommands 从 assistant 消息的 tool_calls 解析 shell 命令
// 返回 map[tool_call_id]command（供 RTK 命令感知压缩）
func extractToolCommands(msgs []chatMessage) map[string]string {
	commands := make(map[string]string)
	for _, msg := range msgs {
		if compression.Role(msg.Role) != compression.RoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			id, _ := tc["id"].(string)
			if id == "" {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			argsStr, _ := fn["arguments"].(string)
			// arguments 是 JSON，解析其中的 command 字段（bash/shell 类工具）
			var args map[string]any
			if json.Unmarshal([]byte(argsStr), &args) == nil {
				if cmd, ok := args["command"].(string); ok && cmd != "" {
					commands[id] = cmd
				}
			}
		}
	}
	return commands
}

// extractBearer 从 Authorization header 提取 Bearer token
// 格式：Authorization: Bearer sk-xxx
func extractBearer(auth string) string {
	const prefix = "Bearer "
	if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return ""
}

// contentOf 取压缩后的内容（没压缩则用原文）
func contentOf(out *compression.Output, original any) any {
	if out != nil && out.Content != nil {
		return out.Content
	}
	return original
}

// newRequestID 生成全局唯一的请求 ID（时间戳 + 原子计数）
func newRequestID() string {
	n := atomic.AddUint64(&reqIDCounter, 1)
	return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), n)
}

// itoa 整数转字符串
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// mustJSON 序列化为 JSON 字符串
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ============ 单组件测试（/test/engine）============

// handleTestEngine 单引擎测试接口：指定引擎 + 输入，返回该引擎独立压缩结果
func handleTestEngine(c *gin.Context) {
	var req struct {
		Engine  string `json:"engine"`
		Content string `json:"content"`
		Query   string `json:"query"`
		Command string `json:"command"` // RTK 命令感知用（shell 命令）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid body: " + err.Error()})
		return
	}

	// prefix 是分区器（非压缩器），单独处理
	if req.Engine == "prefix" {
		handleTestPrefix(c, req.Content)
		return
	}

	out, note, err := testEngine(req.Engine, req.Content, req.Query, req.Command)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{
		"engine":  req.Engine,
		"note":    note,
		"applied": out != nil && out.SavedTokens > 0,
		"original": req.Content,
	}
	if out != nil && out.Content != nil {
		resp["compressed"] = contentString(out.Content)
		resp["saved"] = out.SavedTokens
		resp["original_len"] = out.Metrics.OriginalTokens
		resp["compressed_len"] = out.Metrics.CompressedTokens
		if len(out.Elided) > 0 {
			resp["elided"] = out.Elided
		}
		if len(out.Techniques) > 0 {
			resp["techniques"] = out.Techniques
		}
		if len(out.Rules) > 0 {
			resp["rules"] = out.Rules
		}
		if out.Confidence > 0 {
			resp["confidence"] = out.Confidence
		}
	} else {
		resp["compressed"] = req.Content
		resp["saved"] = 0
		resp["original_len"] = len([]rune(req.Content))
		resp["compressed_len"] = len([]rune(req.Content))
	}
	c.JSON(200, resp)
}

// testEngine 按引擎构造 Input 并调用对应压缩器
func testEngine(engine, content, query, command string) (*compression.Output, string, error) {
	switch engine {
	case "normalize":
		in := &compression.Input{Role: compression.RoleUser, Content: content}
		return applyTest(normalizeC, in), "合并连续空行/空格、去重复标点、trim 首尾（无损），作用于 user 消息", nil
	case "caveman":
		in := &compression.Input{Role: compression.RoleSystem, Content: content}
		return applyTest(promptC, in), "精简系统提示词冗余表达（Caveman 结构化规则，近无损）", nil
	case "rtk":
		in := &compression.Input{Role: compression.RoleTool, Region: compression.RegionHot, Content: content, Command: command, AllowLossy: true}
		return applyTest(rtkGuard, in), "命令感知压缩：知道 shell 命令时按命令过滤器压缩（build/test/git），否则按内容类型（日志/JSON/diff）", nil
	case "gcf":
		in := &compression.Input{Role: compression.RoleTool, Region: compression.RegionHot, Content: content}
		return applyTest(gcfC, in), "同质 JSON 数组转列式编码（无损），输入需为 JSON 数组", nil
	case "ccr":
		in := &compression.Input{Role: compression.RoleAssistant, Region: compression.RegionArchived, Content: content}
		return applyTest(ccrC, in), "超长历史内容（>2000 字符）归档为 [ccr hash=…] 占位、按需取回（可恢复）", nil
	case "llmlingua":
		in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionArchived, Content: content, AllowLossy: true}
		return applyTest(lossyGuard, in), "有损语义压缩：删低信息词（停用词/语气词），保留数字/专名/关键词，保头尾。需护栏授权", nil
	case "omniglyph":
		in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionArchived, Content: content}
		return applyTest(omniglyphC, in), "长文本（>200 字符）渲染成 PNG 图片，换载体省 token。需视觉模型消费", nil
	case "relevance":
		in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionArchived, Content: splitLines(content), Query: query}
		return applyTest(relevanceC, in), "按最后查询对历史句做 Jaccard 评分，只保留相关句（有损，需填查询词）", nil
	case "tool-prune":
		var tools []map[string]any
		if err := json.Unmarshal([]byte(content), &tools); err != nil {
			return nil, "", fmt.Errorf("工具定义需为 JSON 数组: %v", err)
		}
		in := &compression.Input{Role: compression.RoleSystem, TaskType: "agent", Content: toolsToDefs(tools)}
		return applyTest(toolP, in), "按 agent 角色 allow-list 裁剪工具定义 + 精简描述（无损），输入 tools JSON 数组", nil
	case "session":
		in := &compression.Input{Role: compression.RoleTool, Region: compression.RegionHot, Content: splitLines(content)}
		return applyTest(sessionD, in), "会话去重：重复整块内容用 [dup of #N] 替代（近无损），每行一条消息", nil
	case "multimodal":
		var parts []map[string]any
		if err := json.Unmarshal([]byte(content), &parts); err != nil {
			return nil, "", fmt.Errorf("多模态内容需为 JSON 数组: %v", err)
		}
		in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionHot, TaskType: "chat", Content: parts}
		return applyTest(multimodal, in), "图片 detail high→low 降级（省 80%+ 图片 token，低风险），输入多模态数组 JSON", nil
	case "summarize":
		in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionArchived, Content: content, Query: query, AllowLossy: true}
		return applyTest(summarizeGuard, in), "抽取式摘要：保首末句+与查询相关句（本地启发式），输入需 >300 字符", nil
	case "age":
		in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionArchived, Content: content, AllowLossy: true}
		return applyTest(ageGuard, in), "渐进老化：丢中段陈旧内容、保头尾（>400 字符）", nil
	case "lite", "aggressive", "ultra":
		return runPresetTest(engine, content, query, command)
	default:
		return nil, "", fmt.Errorf("未知引擎: %s（可选：normalize/caveman/rtk/gcf/ccr/relevance/tool-prune/session/multimodal/prefix/summarize/age/lite/aggressive/ultra）", engine)
	}
}

// runPresetTest 运行复合预设：把预设展开为原子引擎，依次对输入（按适用性）链式压缩
func runPresetTest(preset, content, query, command string) (*compression.Output, string, error) {
	atoms, ok := presetExpansion[preset]
	if !ok {
		return nil, "", fmt.Errorf("未知预设: %s", preset)
	}
	in := &compression.Input{Role: compression.RoleUser, Region: compression.RegionArchived, Content: content, Query: query, Command: command, AllowLossy: true}
	var out *compression.Output
	lastContent := any(content)
	fired := []string{}
	for _, name := range atoms {
		cp := presetEngineByName(name)
		if cp == nil {
			continue
		}
		cur := *in
		cur.Content = lastContent
		if !cp.IsApplicable(&cur) {
			continue
		}
		o, err := cp.Apply(context.Background(), &cur)
		if err != nil || o == nil || o.Content == nil {
			continue
		}
		fired = append(fired, name)
		lastContent = o.Content
		out = o
	}
	if out == nil {
		return nil, "复合预设 " + preset + " 未对输入生效（可能输入不适用其中任何原子引擎，如 rtk 需工具结果文本）", nil
	}
	return out, "复合预设 " + preset + " 生效引擎: " + strings.Join(fired, " → "), nil
}

// presetEngineByName 按原子引擎名取对应的（已护栏）压缩器实例
func presetEngineByName(name string) compression.Compressor {
	switch name {
	case "normalize":
		return normalizeC
	case "caveman":
		return promptC
	case "tool-prune":
		return toolP
	case "rtk":
		return rtkGuard
	case "gcf":
		return gcfC
	case "relevance":
		return relevanceC
	case "session":
		return sessionD
	case "ccr":
		return ccrC
	case "llmlingua":
		return lossyGuard
	case "omniglyph":
		return omniglyphC
	case "summarize":
		return summarizeGuard
	case "age":
		return ageGuard
	default:
		return nil
	}
}

// applyTest 调用单个压缩器（不适用/失败返回 nil）
func applyTest(cp compression.Compressor, in *compression.Input) *compression.Output {
	if cp == nil || !cp.IsApplicable(in) {
		return nil
	}
	out, err := cp.Apply(context.Background(), in)
	if err != nil {
		return nil
	}
	return out
}

// contentString 把任意类型的内容转成可展示字符串
func contentString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return mustJSON(t)
	}
}

// splitLines 按行拆分（兼容 \r\n）
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

// handleTestPrefix 前缀对齐分区测试：输入 messages JSON 数组，输出三区统计
func handleTestPrefix(c *gin.Context, content string) {
	var msgs []map[string]any
	if err := json.Unmarshal([]byte(content), &msgs); err != nil {
		c.JSON(400, gin.H{"error": "messages 需为 JSON 数组: " + err.Error()})
		return
	}
	p := partitioner.Partition(msgs)
	c.JSON(200, gin.H{
		"engine": "prefix",
		"note":   "前缀对齐三区分区：冻结=开头 system、封存=历史轮次、热区=最后 user 及其后；在冻结/封存边界注入 cache_control 断点",
		"applied": true,
		"zones": gin.H{
			"frozen":   len(p.Frozen),
			"archived": len(p.Archived),
			"hot":      len(p.Hot),
		},
		"frozen":   p.Frozen,
		"archived": p.Archived,
		"hot":      p.Hot,
	})
}

// loadEnv 从 .env 文件加载 KEY=VALUE（已存在的环境变量优先，不覆盖）
func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // 没有 .env 不报错
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

// handleHome 返回可视化首页（从 web/index.html 嵌入）
func handleHome(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		c.String(500, "index.html not found: "+err.Error())
		return
	}
	c.String(200, string(html))
}

