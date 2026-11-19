// service/compression/toolprune.go
// 工具定义裁剪：Agent 场景下，按角色 allow-list 过滤 tools，压缩 inputSchema。
// 对应计划 G12（无损）。
//
// 设计要点：
//   - 无损：只删"该角色用不上的工具"，保留的工具定义原样
//   - allow-list：每个角色允许用哪些工具（如 code 角色只留读代码的工具）
//   - inputSchema 压缩：去掉 description 里的冗余（保留字段名和类型）
//
// 为什么有用：MCP 50 工具 × 每次请求 = 2000-5000 token 仅工具定义；
// 工具越多模型越难选对（>20 工具准确率下降）。

package compression

import (
	"context"
	"strings"
)

// 工具定义（OpenAI tools 数组的元素）
type ToolDef struct {
	Type     string         `json:"type"`
	Function ToolFunction   `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolPruner 工具定义裁剪器（无损）
type ToolPruner struct {
	// 每个角色的允许工具名单（name 前缀匹配）
	allowLists map[string][]string
}

// NewToolPruner 创建工具裁剪器，内置默认角色名单
func NewToolPruner() *ToolPruner {
	return &ToolPruner{
		allowLists: map[string][]string{
			// code 角色：只留代码相关工具
			"code": {"read_file", "search_code", "list_files", "git_"},
			// chat 角色：只留通用工具
			"chat": {"web_search", "calculator", "weather"},
			// agent 角色：有 tools 的默认角色，留核心工具 + 代码工具
			"agent": {"read_file", "search_code", "list_files", "git_", "web_search", "calculator"},
			// 其他角色：默认全留（不裁剪，保护）
		},
	}
}

// Name 实现 Compressor 接口
func (t *ToolPruner) Name() string { return "tool-prune" }

// Stage 实现 Compressor 接口：无损，S0
func (t *ToolPruner) Stage() Stage { return S0 }

// Mode 实现 Compressor 接口
func (t *ToolPruner) Mode() Mode { return FailOpen }

// OutputKind 实现 Compressor 接口
func (t *ToolPruner) OutputKind() OutputKind { return KindStructured }

// IsApplicable 实现 Compressor 接口：内容是可用的工具列表
func (t *ToolPruner) IsApplicable(in *Input) bool {
	if in == nil || in.Role != RoleSystem {
		return false
	}
	_, ok := in.Content.([]ToolDef)
	return ok
}

// Apply 实现 Compressor 接口：按角色裁剪工具
func (t *ToolPruner) Apply(ctx context.Context, in *Input) (*Output, error) {
	tools, ok := in.Content.([]ToolDef)
	if !ok {
		return FailOpenOutput(in), nil
	}

	// 找该角色允许的工具（用 TaskType 当角色）
	allow := t.allowLists[in.TaskType]
	// 没有配置的角色 → 不裁剪（保护）
	if len(allow) == 0 {
		return FailOpenOutput(in), nil
	}

	var kept []ToolDef
	for _, tool := range tools {
		if t.isAllowed(tool.Function.Name, allow) {
			// 压缩 inputSchema 的 description（去掉冗余描述）
			tool.Function.Description = t.tightenDescription(tool.Function.Description)
			kept = append(kept, tool)
		}
	}

	// 一个都没留下（全被裁剪）→ 保护：保留原文
	if len(kept) == 0 {
		return FailOpenOutput(in), nil
	}

	// 算省了多少（用工具数估算）
	savedTokens := len(tools) - len(kept)
	out := &Output{
		Content:     kept,
		SavedTokens: savedTokens,
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       t.Name(),
			OriginalTokens:   len(tools),
			CompressedTokens: len(kept),
			SavedTokens:      savedTokens,
		},
	}
	out.Hash = hashContent(kept)
	return out, nil
}

// isAllowed 判断工具是否在允许名单（前缀匹配）
func (t *ToolPruner) isAllowed(name string, allow []string) bool {
	for _, prefix := range allow {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// tightenDescription 精简工具描述：去掉冗长的祈使句前缀，保留核心
func (t *ToolPruner) tightenDescription(desc string) string {
	if len(desc) <= 80 {
		return desc // 短描述不动
	}
	// 去掉"Use this tool to ..."这类冗余开头，保留下半句
	lower := strings.ToLower(desc)
	if strings.HasPrefix(lower, "use this tool to ") {
		return strings.TrimSpace(desc[len("Use this tool to "):])
	}
	if strings.HasPrefix(lower, "use this ") {
		idx := strings.Index(lower, " to ")
		if idx > 0 && idx < len(desc) {
			return strings.TrimSpace(desc[idx+4:])
		}
	}
	return desc
}
