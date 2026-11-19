// plugin.go
// 插件化压缩引擎：每个压缩器是独立插件，按请求指定启用，不写死。
// 借鉴 DeepSeek Harness "Everything is a Plugin" 概念。
//
// 设计：
//   - 插件注册表：map[名字]Plugin，每个插件独立开关
//   - 按需启用：请求体 compression.plugins = ["caveman","rtk",...]
//   - 有损级别控制：管理员设 max_lossy，用户请求不能超过（取较低）
//   - 用户不指定 → 用管理员默认策略

package main

import (
	"context"
	"sync"

	"token-comper-demo/service/compression"
)

// PluginMeta 插件元信息
type PluginMeta struct {
	Name     string // 插件名（caveman / rtk / tool-prune / multimodal / prefix / session）
	Desc     string // 描述
	Lossy    bool   // 是否有损（有损受 max_lossy 控制）
	Intensity bool  // 是否支持强度级别（lite/full/ultra）
}

// pluginRegistry 插件注册表（不写死，可增删）
var pluginRegistry = map[string]PluginMeta{
	"caveman":   {Name: "caveman", Desc: "系统提示词精简（删客套/冗余）", Lossy: false, Intensity: true},
	"rtk":       {Name: "rtk", Desc: "工具结果按类型压缩（日志/JSON/diff/grep）", Lossy: true},
	"tool-prune": {Name: "tool-prune", Desc: "按角色裁剪工具定义", Lossy: false},
	"multimodal": {Name: "multimodal", Desc: "多模态图片 detail 降级", Lossy: false},
	"normalize":  {Name: "normalize", Desc: "无损格式清理（合并空行/空格/重复标点）", Lossy: false},
	"prefix":     {Name: "prefix", Desc: "前缀对齐三区分区", Lossy: false},
	"session":    {Name: "session", Desc: "会话去重（重复内容替换）", Lossy: false},
	"gcf":        {Name: "gcf", Desc: "同质 JSON 数组转列式编码", Lossy: false},
	"ccr":        {Name: "ccr", Desc: "历史长内容归档占位、按需取回", Lossy: true},
	"relevance":  {Name: "relevance", Desc: "按最后查询过滤历史不相关句", Lossy: true},
	"llmlingua":  {Name: "llmlingua", Desc: "有损语义压缩（删低信息词，需护栏）", Lossy: true},
	"omniglyph":  {Name: "omniglyph", Desc: "长文本转图片（换载体省 token）", Lossy: false},
	// 历史压缩原子引擎（aggressive / ultra 复合预设会用到）
	"summarize": {Name: "summarize", Desc: "抽取式摘要（保首末句+相关句，本地启发式）", Lossy: true},
	"age":       {Name: "age", Desc: "渐进老化（丢中段陈旧内容，保头尾）", Lossy: true},
	// 复合预设（自动展开为下方原子引擎组合，见 presetExpansion）
	"lite":       {Name: "lite", Desc: "复合预设：空白清理+系统提示精简+工具定义裁剪+列式（无损）", Lossy: false, Intensity: false},
	"aggressive": {Name: "aggressive", Desc: "复合预设：工具压缩+摘要+老化+相关性+归档（含全部有损）", Lossy: true, Intensity: false},
	"ultra":      {Name: "ultra", Desc: "复合预设：aggressive + 语义剪枝 + 文本转图（最高强度）", Lossy: true, Intensity: false},
	"all":        {Name: "all", Desc: "启用全部无损插件", Lossy: false},
}

// presetExpansion 复合预设 → 原子引擎列表（对应 OmniRoute 的 lite/aggressive/ultra 内部流水线）
// 展开后由 resolvePlugins 统一做有损过滤 + 去重。
var presetExpansion = map[string][]string{
	"lite":       {"normalize", "caveman", "tool-prune", "gcf"},
	"aggressive": {"normalize", "caveman", "tool-prune", "gcf", "rtk", "relevance", "session", "ccr", "summarize", "age", "llmlingua"},
	"ultra":      {"normalize", "caveman", "tool-prune", "gcf", "rtk", "relevance", "session", "ccr", "summarize", "age", "llmlingua", "omniglyph"},
}

// AdminPolicy 管理员策略（控制有损上限 + 默认插件）
type AdminPolicy struct {
	MaxLossy       bool     `json:"max_lossy"`         // 是否允许有损压缩
	DefaultPlugins []string `json:"default_plugins"`   // 用户不指定时默认启用哪些
	IntensityCeil  string   `json:"intensity_ceil"`    // 强度上限（lite/full/ultra），用户不能超
}

// defaultPolicy 默认管理员策略（.env 可覆盖初始值，/admin 页面可运行时修改）
var defaultPolicy = AdminPolicy{
	MaxLossy:       false, // 默认不允许有损（安全）
	DefaultPlugins: []string{"normalize", "caveman", "prefix", "tool-prune", "gcf"},
	IntensityCeil:  "full",
}

// currentPolicy 当前生效策略（管理员页面修改后更新，存内存）
// 用读写锁保护：handleCompress/handleGetPolicy 并发读，handleSetPolicy 写，避免 data race。
var (
	currentPolicy = defaultPolicy
	policyMu      sync.RWMutex
)

// getCurrentPolicy 返回当前策略的副本（并发安全）
func getCurrentPolicy() AdminPolicy {
	policyMu.RLock()
	defer policyMu.RUnlock()
	return currentPolicy
}

// setCurrentPolicy 更新当前策略（并发安全）
func setCurrentPolicy(p AdminPolicy) {
	policyMu.Lock()
	currentPolicy = p
	policyMu.Unlock()
}

// resolvePlugins 根据 API key 的压缩策略 + 管理员策略，解析最终要启用的插件列表
// 规则：
//   1. key 没配置 → 管理员默认
//   2. 有损插件仅在 key 允许 AND 管理员允许时才启用
//   3. all = 全部无损插件
func resolvePlugins(key *KeyCompression, policy AdminPolicy) []PluginMeta {
	var names []string
	if key != nil && len(key.Plugins) > 0 {
		names = key.Plugins
	} else {
		names = policy.DefaultPlugins
	}

	// 展开 "all" 与复合预设（lite/aggressive/ultra）
	expanded := []string{}
	for _, n := range names {
		switch n {
		case "all":
			for _, m := range pluginRegistry {
				if !m.Lossy {
					expanded = append(expanded, m.Name)
				}
			}
		default:
			if preset, ok := presetExpansion[n]; ok {
				expanded = append(expanded, preset...)
			} else {
				expanded = append(expanded, n)
			}
		}
	}
	// 去重（复合预设可能和显式列表重复）
	seen := map[string]bool{}
	dedup := expanded[:0]
	for _, n := range expanded {
		if !seen[n] {
			seen[n] = true
			dedup = append(dedup, n)
		}
	}
	expanded = dedup

	// 有损过滤：
	//   - 有 key：需要 key.Lossy AND 管理员 MaxLossy
	//   - 无 key：只需管理员 MaxLossy（走管理员默认策略，此时管理员开关即最终决定）
	lossyAllowed := policy.MaxLossy && (key == nil || key.Lossy)
	var result []PluginMeta
	for _, name := range expanded {
		meta, ok := pluginRegistry[name]
		if !ok {
			continue // 未知插件跳过
		}
		if meta.Lossy && !lossyAllowed {
			continue
		}
		result = append(result, meta)
	}
	return result
}

// clampIntensity 强度取"用户指定"和"管理员上限"中较低者
// lite(0) < full(1) < ultra(2)
func clampIntensity(user string, ceil string) compression.Intensity {
	ui := compression.IntensityFromString(user)
	ci := compression.IntensityFromString(ceil)
	if ui > ci {
		return ci
	}
	return ui
}

// runPlugin 执行单个插件，返回节省（trace 可选，传 nil 不记录级联）
func runPlugin(p PluginMeta, in *compression.Input, intensity compression.Intensity, saved map[string]int, steps *[]string, trace *[]EngineTraceEntry) *compression.Output {
	switch p.Name {
	case "caveman":
		c := compression.NewPromptCompressor().WithIntensity(intensity)
		return applyWithMeter(c, in, saved, steps, meter, trace)
	case "rtk":
		return applyWithMeter(rtkGuard, in, saved, steps, meter, trace)
	case "tool-prune":
		return applyWithMeter(toolP, in, saved, steps, meter, trace)
	case "multimodal":
		return applyWithMeter(multimodal, in, saved, steps, meter, trace)
	case "normalize":
		return applyWithMeter(normalizeC, in, saved, steps, meter, trace)
	case "session":
		return applyWithMeter(sessionD, in, saved, steps, meter, trace)
	case "gcf":
		return applyWithMeter(gcfC, in, saved, steps, meter, trace)
	case "llmlingua":
		return applyWithMeter(lossyGuard, in, saved, steps, meter, trace)
	case "omniglyph":
		return applyWithMeter(omniglyphC, in, saved, steps, meter, trace)
	case "summarize":
		return applyWithMeter(summarizeGuard, in, saved, steps, meter, trace)
	case "age":
		return applyWithMeter(ageGuard, in, saved, steps, meter, trace)
	default:
		return nil
	}
}

// 占位：applyWithMeter 在 main.go 定义，这里通过包级引用
var _ = context.Background
