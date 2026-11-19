// service/compression/rtk.go
// RTK 工具结果压缩：检测工具返回内容的类型，按类型做针对性压缩。
// 对应计划 C5（有损，需护栏）。
//
// 有损压缩：会删内容（日志 INFO 行），Stage = S2（有损级），依赖护栏包裹。
// 安全红线：普通文本（未知类型）不压缩，保护用户内容。

package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 类型检测正则
var (
	logLinePattern = regexp.MustCompile(`(?i)\b(INFO|ERROR|WARNING|DEBUG|TRACE)\b`)
	diffPattern    = regexp.MustCompile(`(?m)^(diff|---|index [0-9a-f]{7})`)
	grepPattern    = regexp.MustCompile(`(?m)^[^\s:]+:\d+:`)
	// ANSI 颜色码：\x1b[31m 这种
	ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// 代码文件特征：含 func/def/class/import 等代码关键字
	codePattern = regexp.MustCompile(`(?m)^\s*(?:func|func |def |class |import |package |const |var |type )`)
)

// ContentType 内容类型
type ContentType int

const (
	TypeUnknown     ContentType = iota // 未知类型（不压缩，保护）
	TypeLog                            // 日志
	TypeJSON                           // JSON
	TypeDiff                           // git
	TypeGrep                           // grep 搜索结果
	TypeFileContent                    // 代码文件
	TypeShell                          // shell 命令输出
)

// RTKCompressor 工具结果压缩器（有损，需护栏）
type RTKCompressor struct{}

// NewRTKCompressor 创建 RTK 压缩器
func NewRTKCompressor() *RTKCompressor { return &RTKCompressor{} }

// Name 实现 Compressor 接口
func (r *RTKCompressor) Name() string { return "rtk" }

// Stage 实现 Compressor 接口：有损，S2
func (r *RTKCompressor) Stage() Stage { return S2 }

// Mode 实现 Compressor 接口：有损必须 fail-closed（宁可失败也不输出坏结果）
func (r *RTKCompressor) Mode() Mode { return FailClosed }

// OutputKind 实现 Compressor 接口
func (r *RTKCompressor) OutputKind() OutputKind { return KindStructured }

// IsApplicable 实现 Compressor 接口：只处理热区工具结果
func (r *RTKCompressor) IsApplicable(in *Input) bool {
	if in == nil || in.Role != RoleTool || in.Region != RegionHot {
		return false
	}
	content, ok := in.Content.(string)
	if !ok {
		return false
	}
	return detectType(content) != TypeUnknown
}

// detectType 判断内容类型
func detectType(content string) ContentType {
	var v any
	if json.Unmarshal([]byte(content), &v) == nil {
		return TypeJSON
	}
	if logLinePattern.MatchString(content) {
		return TypeLog
	}
	if diffPattern.MatchString(content) {
		return TypeDiff
	}
	if grepPattern.MatchString(content) {
		return TypeGrep
	}
	// 含 ANSI 颜色码 → shell 输出
	if ansiPattern.MatchString(content) {
		return TypeShell
	}
	// 含代码关键字 → 代码文件
	if codePattern.MatchString(content) {
		return TypeFileContent
	}
	return TypeUnknown
}

// Apply 实现 Compressor 接口：按类型压缩
func (r *RTKCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	if in == nil {
		return nil, ErrNilInput
	}
	content, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	// 命令感知：知道 shell 命令 → 按命令类型过滤器压缩（比只看内容类型更精准）
	if in.Command != "" {
		if detection := detectCommand(in.Command, content); detection != nil {
			if filter := rtkFilters[detection.Type]; filter != nil {
				return r.compressByFilter(content, filter, detection), nil
			}
		}
	}

	t := detectType(content)
	switch t {
	case TypeJSON:
		// JSON 是结构化数据，走专门的 JSON 压缩，不做只读区保护（避免破坏 JSON 结构）
		return r.compressJSON(content), nil
	case TypeUnknown:
		return FailOpenOutput(in), nil // 未知类型：不压缩
	}

	// 只读区保护（H14）：代码块/URL/路径/命令/数字先占位，压缩后再还原，
	// 保证这些字节不被压缩器误删/改写。
	protector := NewReadOnlyProtector()
	protected := protector.Protect(content)

	var out *Output
	switch t {
	case TypeLog:
		out = r.compressLog(protected)
	case TypeDiff:
		out = r.compressDiff(protected)
	case TypeGrep:
		out = r.compressGrep(protected)
	case TypeFileContent:
		out = r.compressFile(protected)
	case TypeShell:
		out = r.compressShell(protected)
	}

	// 还原只读区，并重算节省
	if out != nil {
		if s, ok := out.Content.(string); ok {
			final := protector.Restore(s)
			out.Content = final
			out.SavedTokens = max0(len([]rune(content)) - len([]rune(final)))
			out.Metrics.CompressedTokens = len([]rune(final))
			out.Metrics.SavedTokens = out.SavedTokens
		}
	}
	return out, nil
}

// compressByFilter 按命令过滤器压缩（keep/drop/collapse 规则）
func (r *RTKCompressor) compressByFilter(content string, filter *rtkFilter, detection *CommandDetection) *Output {
	clean := content
	if filter.StripAnsi {
		clean = ansiPattern.ReplaceAllString(content, "")
	}

	lines := strings.Split(clean, "\n")
	var kept []string
	var collapsed []string
	dropped := 0

	for _, line := range lines {
		// keep 优先级最高
		if matchesAny(line, filter.KeepPatterns) {
			kept = append(kept, line)
			continue
		}
		// drop
		if matchesAny(line, filter.DropPatterns) {
			dropped++
			continue
		}
		// collapse：折叠匹配行（保留首行 + 计数）
		if matchesAny(line, filter.CollapsePatterns) {
			collapsed = append(collapsed, line)
			continue
		}
		kept = append(kept, line)
	}

	// 折叠行合并为一行 + 计数
	if len(collapsed) > 1 {
		kept = append(kept, fmt.Sprintf("%s\n[%d similar lines collapsed]", collapsed[0], len(collapsed)-1))
	} else if len(collapsed) == 1 {
		kept = append(kept, collapsed[0])
	}

	out := strings.Join(kept, "\n")
	saved := max0(len([]rune(content)) - len([]rune(out)))

	// 透明标记
	var elided []Elision
	if dropped > 0 {
		elided = append(elided, Elision{Kind: "lines", Count: dropped, Reason: "rtk filter: dropped by " + detection.Type})
	}
	if len(collapsed) > 1 {
		elided = append(elided, Elision{Kind: "lines", Count: len(collapsed) - 1, Reason: "rtk filter: collapsed by " + detection.Type})
	}

	return &Output{
		Content:     out,
		SavedTokens: saved,
		OutputKind:  KindStructured,
		Techniques:  []string{"rtk-filter", "command-aware"},
		Rules:       []string{detection.Type + ":keep", detection.Type + ":drop"},
		Confidence:  detection.Confidence,
		Elided:      elided,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(out)),
			SavedTokens:      saved,
		},
	}
}

// compressLog 日志压缩：保留 ERROR/WARNING/FATAL，删 INFO/DEBUG
func (r *RTKCompressor) compressLog(content string) *Output {
	lines := strings.Split(content, "\n")
	var kept []string
	for _, line := range lines {
		up := strings.ToUpper(line)
		if strings.Contains(up, "ERROR") || strings.Contains(up, "WARNING") || strings.Contains(up, "FATAL") {
			kept = append(kept, line)
		}
	}
	// 全是 INFO（一条错误信号都没有）→ 返回原文，防过度压缩
	if len(kept) == 0 {
		kept = lines
	}
	// 省略标记：如果删了行，告诉模型省掉了多少（绝不静默删）
	deleted := len(lines) - len(kept)
	if deleted > 0 {
		kept = append(kept, fmt.Sprintf("[%d lines elided]", deleted))
	}
	out := strings.Join(kept, "\n")
	return &Output{
		Content:     out,
		SavedTokens: max0(len([]rune(content)) - len([]rune(out))),
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(out)),
			SavedTokens:      max0(len([]rune(content)) - len([]rune(out))),
		},
	}
}

// compressJSON JSON 压缩：短 JSON 去空白；长数组保前5后2 + 摘要；对象保前20 key
func (r *RTKCompressor) compressJSON(content string) *Output {
	var v any
	if json.Unmarshal([]byte(content), &v) != nil {
		return &Output{Content: content, SavedTokens: 0, OutputKind: KindStructured}
	}

	// 太短不压（<2000 字符直接紧凑输出，不细分）
	if len(content) < 2000 {
		compact, _ := json.Marshal(v)
		out := string(compact)
		saved := max0(len([]rune(content)) - len([]rune(out)))
		return &Output{
			Content:     out,
			SavedTokens: saved,
			OutputKind:  KindStructured,
			Metrics: Metrics{
				EngineName:       r.Name(),
				OriginalTokens:   len([]rune(content)),
				CompressedTokens: len([]rune(out)),
				SavedTokens:      saved,
			},
		}
	}

	// 长 JSON：区分数组 / 对象
	switch arr := v.(type) {
	case []any:
		// 数组：保前5后2 + 摘要
		return r.compressJSONArray(content, arr)
	case map[string]any:
		// 对象：保前 20 key + 嵌套值摘要
		return r.compressJSONObject(content, arr)
	default:
		// 其他（字符串/数字/布尔）→ 紧凑
		compact, _ := json.Marshal(v)
		return &Output{Content: string(compact), SavedTokens: max0(len([]rune(content)) - len(compact)), OutputKind: KindStructured}
	}
}

// compressJSONArray 数组摘要：保前5后2 + total + 标记
func (r *RTKCompressor) compressJSONArray(content string, arr []any) *Output {
	total := len(arr)
	first5 := arr
	last2 := []any(nil)
	if total > 5 {
		first5 = arr[:5]
		last2 = arr[total-2:]
	}
	summary := map[string]any{
		"type":    "array",
		"total":   total,
		"first5":  first5,
		"last2":   last2,
	}
	out, _ := json.Marshal(summary)
	return &Output{
		Content:     string(out),
		SavedTokens: max0(len([]rune(content)) - len([]rune(string(out)))),
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(string(out))),
			SavedTokens:      max0(len([]rune(content)) - len([]rune(string(out)))),
		},
	}
}

// compressJSONObject 对象摘要：保前 20 key + 嵌套值摘要
func (r *RTKCompressor) compressJSONObject(content string, obj map[string]any) *Output {
	// 保前 20 个 key（map 无序，用 keys 排序保证稳定）
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	// 简化：只取前 20 个 key（demo 阶段不排序，保持简单）
	const maxKeys = 20
	summary := map[string]any{}
	i := 0
	for k, val := range obj {
		if i >= maxKeys {
			break
		}
		// 嵌套值摘要：对象/数组只标类型，不展开
		summary[k] = summarizeValue(val)
		i++
	}
	out, _ := json.Marshal(summary)
	return &Output{
		Content:     string(out),
		SavedTokens: max0(len([]rune(content)) - len([]rune(string(out)))),
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(string(out))),
			SavedTokens:      max0(len([]rune(content)) - len([]rune(string(out)))),
		},
	}
}

// summarizeValue 把嵌套值摘要为"类型+长度"，不展开
func summarizeValue(v any) any {
	switch t := v.(type) {
	case []any:
		return map[string]any{"type": "array", "len": len(t)}
	case map[string]any:
		return map[string]any{"type": "object", "keys": len(t)}
	default:
		return v
	}
}

// compressDiff diff 压缩：压缩连续空行
func (r *RTKCompressor) compressDiff(content string) *Output {
	lines := strings.Split(content, "\n")
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" && len(kept) > 0 {
			if strings.TrimSpace(kept[len(kept)-1]) == "" {
				continue
			}
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	return &Output{
		Content:     out,
		SavedTokens: max0(len([]rune(content)) - len([]rune(out))),
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(out)),
			SavedTokens:      max0(len([]rune(content)) - len([]rune(out))),
		},
	}
}
// compressGrep grep 搜索结果压缩：保前 30 条 + 省略标记
func (r *RTKCompressor) compressGrep(content string) *Output {
	lines := strings.Split(content, "\n")
	keepN := 30 // 最多保留前 30 条
	var kept []string
	for i := 0; i < len(lines) && i < keepN; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		kept = append(kept, lines[i])
	}

	deleted := len(lines) - len(kept)
	if deleted > 0 {
		kept = append(kept, fmt.Sprintf("[%d more matches]", deleted))
	}

	out := strings.Join(kept, "\n")
	saved := max0(len([]rune(content)) - len([]rune(out)))
	return &Output{
		Content:     out,
		SavedTokens: saved,
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(out)),
			SavedTokens:      saved,
		},
	}
}

// compressFile 代码文件压缩：保前 20 行 + 后 5 行 + 省略标记
func (r *RTKCompressor) compressFile(content string) *Output {
	lines := strings.Split(content, "\n")
	const keepHead = 20 // 前 20 行
	const keepTail = 5  // 后 5 行

	// 太短不压（防过度压缩）
	if len(lines) <= keepHead+keepTail {
		return &Output{Content: content, SavedTokens: 0, OutputKind: KindStructured}
	}

	var kept []string
	kept = append(kept, lines[:keepHead]...)
	deleted := len(lines) - keepHead - keepTail
	kept = append(kept, fmt.Sprintf("[%d lines elided]", deleted))
	kept = append(kept, lines[len(lines)-keepTail:]...)

	out := strings.Join(kept, "\n")
	return &Output{
		Content:     out,
		SavedTokens: max0(len([]rune(content)) - len([]rune(out))),
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(out)),
			SavedTokens:      max0(len([]rune(content)) - len([]rune(out))),
		},
	}
}

// compressShell shell 输出压缩：剥 ANSI 颜色码 + 保尾 50 行 + 省略标记
func (r *RTKCompressor) compressShell(content string) *Output {
	// 1. 先剥掉颜色码（模型读不懂 \x1b[31m）
	clean := ansiPattern.ReplaceAllString(content, "")

	lines := strings.Split(clean, "\n")
	const keepTail = 50 // 保尾 50 行

	// 太短不压
	if len(lines) <= keepTail {
		return &Output{Content: clean, SavedTokens: 0, OutputKind: KindStructured}
	}

	// 保尾 50 行 + 省略标记
	deleted := len(lines) - keepTail
	var kept []string
	kept = append(kept, fmt.Sprintf("[%d lines elided]", deleted))
	kept = append(kept, lines[len(lines)-keepTail:]...)

	out := strings.Join(kept, "\n")
	return &Output{
		Content:     out,
		SavedTokens: max0(len([]rune(content)) - len([]rune(out))),
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(out)),
			SavedTokens:      max0(len([]rune(content)) - len([]rune(out))),
		},
	}
}

// max0 返回非负值（防止算出负数）
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}