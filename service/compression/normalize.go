// service/compression/normalize.go
// 无损格式清理（normalize）：合并连续空行/空格、去重复标点、trim 首尾空白。
// 作用于 user 消息（用户输入）——这类冗余属于「去空白/格式/重复」的无损压缩，
// 不改变语义，可安全清理。
//
// 红线：
//   - 只做格式清理，绝不删词/改词（Stage = S0 无损）。
//   - 代码块 / URL / 路径 / 命令 / 数字用 ReadOnlyProtector 占位保护，不被误改。

package compression

import (
	"context"
	"regexp"
	"strings"
)

var (
	// 连续空格/制表符 → 单个空格
	spaceRe = regexp.MustCompile(`[ \t]+`)
	// 连续换行（2+）→ 单个换行（多余空行合并）
	newlineRe = regexp.MustCompile(`\n{2,}`)
	// 重复标点（2+）→ 单个（逐个标点写规则，因 Go RE2 不支持反向引用）
	punctCollapse = []struct {
		re *regexp.Regexp
		to string
	}{
		{regexp.MustCompile(`，{2,}`), "，"},
		{regexp.MustCompile(`。{2,}`), "。"},
		{regexp.MustCompile(`！{2,}`), "！"},
		{regexp.MustCompile(`？{2,}`), "？"},
		{regexp.MustCompile(`、{2,}`), "、"},
		{regexp.MustCompile(`；{2,}`), "；"},
		{regexp.MustCompile(`：{2,}`), "："},
		{regexp.MustCompile(`…{2,}`), "…"},
		{regexp.MustCompile(`,{2,}`), ","},
		{regexp.MustCompile(`!{2,}`), "!"},
		{regexp.MustCompile(`\?{2,}`), "?"},
		{regexp.MustCompile(`;{2,}`), ";"},
		{regexp.MustCompile(`:{2,}`), ":"},
	}
)

// NormalizeCompressor 无损格式清理压缩器
type NormalizeCompressor struct{}

// NewNormalizeCompressor 创建格式清理器
func NewNormalizeCompressor() *NormalizeCompressor { return &NormalizeCompressor{} }

func (n *NormalizeCompressor) Name() string          { return "normalize" }
func (n *NormalizeCompressor) Stage() Stage           { return S0 } // 完全无损
func (n *NormalizeCompressor) Mode() Mode             { return FailOpen }
func (n *NormalizeCompressor) OutputKind() OutputKind { return KindProse }

// IsApplicable 只处理 user 消息的纯文本内容
func (n *NormalizeCompressor) IsApplicable(in *Input) bool {
	if in == nil || in.Role != RoleUser {
		return false
	}
	_, ok := in.Content.(string)
	return ok
}

// Apply 执行格式清理（带只读区保护）
func (n *NormalizeCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	content, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	// 只读区保护：代码块/URL/路径/命令/数字先占位，清理后再还原
	protector := NewReadOnlyProtector()
	protected := protector.Protect(content)

	normalized := normalizeText(protected)
	final := protector.Restore(normalized)

	if final == content {
		return FailOpenOutput(in), nil // 没有可清理的冗余，返回原文
	}

	saved := max0(len([]rune(content)) - len([]rune(final)))
	out := &Output{
		Content:       final,
		SavedTokens:   saved,
		OutputKind:    KindProse,
		Deterministic: true,
		Metrics: Metrics{
			EngineName:       n.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(final)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(final)
	return out, nil
}

// normalizeText 清理格式冗余（不含只读区，因已占位）
func normalizeText(s string) string {
	// 统一换行符
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// 合并连续空格/制表符
	s = spaceRe.ReplaceAllString(s, " ")
	// 合并连续换行
	s = newlineRe.ReplaceAllString(s, "\n")
	// 合并重复标点
	for _, p := range punctCollapse {
		s = p.re.ReplaceAllString(s, p.to)
	}
	return strings.TrimSpace(s)
}
