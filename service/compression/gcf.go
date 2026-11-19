// service/compression/gcf.go
// GCF（Generic Columnar Format）列式表压缩：对应计划 F11 第①部分。
//
// 工具返回的同质 JSON 数组 → 无损列式编码：
//   列名只写一次，后续每行只写值；嵌套对象 flatten 为 customer>name 路径列。
// 示例： [{"name":"Alice","age":30},{"name":"Bob","age":25}]
//    => "## [2]: name,age\nAlice,30\nBob,25"
// 红线：只对同质数组生效，schema 检测不到就 fail-closed 返回原文。

package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// GCFCompressor 列式表压缩器（结构化无损，S1）
type GCFCompressor struct{}

// NewGCFCompressor 创建列式压缩器
func NewGCFCompressor() *GCFCompressor { return &GCFCompressor{} }

func (g *GCFCompressor) Name() string          { return "gcf" }
func (g *GCFCompressor) Stage() Stage           { return S1 }
func (g *GCFCompressor) Mode() Mode             { return FailClosed }
func (g *GCFCompressor) OutputKind() OutputKind { return KindStructured }

// IsApplicable 内容是工具结果里的同质 JSON 数组
func (g *GCFCompressor) IsApplicable(in *Input) bool {
	if in == nil || in.Role != RoleTool {
		return false
	}
	content, ok := in.Content.(string)
	if !ok {
		return false
	}
	return isHomogeneousArray(content)
}

// isHomogeneousArray 判断字符串是否为"所有元素都是同构对象"的 JSON 数组
// 同构 = flatten 后的列完全一致，与 Apply 的检测保持一致（避免 IsApplicable 误判）
func isHomogeneousArray(content string) bool {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(content), &arr); err != nil || len(arr) == 0 {
		return false
	}
	firstCols := flattenKeys(arr[0], "")
	sort.Strings(firstCols)
	for _, row := range arr[1:] {
		cols := flattenKeys(row, "")
		sort.Strings(cols)
		if !equalStrings(firstCols, cols) {
			return false
		}
	}
	return true
}

// Apply 执行列式编码
func (g *GCFCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	content, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	encoded, cols, err := encodeColumns(content)
	if err != nil {
		return FailOpenOutput(in), nil
	}

	saved := max0(len([]rune(content)) - len([]rune(encoded)))
	out := &Output{
		Content:     encoded,
		SavedTokens: saved,
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       g.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(encoded)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(encoded)
	_ = cols
	return out, nil
}

// encodeColumns 把同质对象数组编码为列式文本
func encodeColumns(content string) (string, []string, error) {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(content), &arr); err != nil {
		return "", nil, err
	}
	if len(arr) == 0 {
		return "", nil, fmt.Errorf("empty array")
	}

	// 用第一个元素确定列（flatten 后的路径），排序保证稳定
	firstCols := flattenKeys(arr[0], "")
	sort.Strings(firstCols)

	// 校验所有元素同构（flatten 后列完全一致）
	for _, row := range arr {
		cols := flattenKeys(row, "")
		sort.Strings(cols)
		if !equalStrings(firstCols, cols) {
			return "", nil, fmt.Errorf("heterogeneous array")
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## [%d]: %s", len(arr), strings.Join(firstCols, ","))
	for _, row := range arr {
		sb.WriteString("\n")
		vals := make([]string, 0, len(firstCols))
		for _, col := range firstCols {
			vals = append(vals, encodeValue(lookupPath(row, col)))
		}
		sb.WriteString(strings.Join(vals, ","))
	}
	return sb.String(), firstCols, nil
}

// flattenKeys 递归把嵌套对象 flatten 为 "a>b" 路径列
func flattenKeys(v any, prefix string) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return []string{prefix}
	}
	var keys []string
	for k, val := range m {
		full := k
		if prefix != "" {
			full = prefix + ">" + k
		}
		if nested, ok := val.(map[string]any); ok && len(nested) > 0 {
			keys = append(keys, flattenKeys(nested, full)...)
		} else {
			keys = append(keys, full)
		}
	}
	return keys
}

// lookupPath 按 "a>b" 路径取嵌套值
func lookupPath(m map[string]any, path string) any {
	parts := strings.Split(path, ">")
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = mm[p]
		if !ok {
			return nil
		}
	}
	return cur
}

// encodeValue 把值编码为无逗号歧义的字符串（标量直接，嵌套用短 JSON）
func encodeValue(v any) string {
	switch t := v.(type) {
	case string:
		if strings.ContainsAny(t, ",\n") {
			b, _ := json.Marshal(t)
			return string(b)
		}
		return t
	case nil:
		return ""
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// equalStrings 比较两个字符串切片是否相等（元素级）
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
