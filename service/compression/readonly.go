// service/compression/readonly.go
// 只读区保护（H14 落地）：识别内容中的代码块 / URL / 路径 / 命令 / 数字，
// 压缩前用占位符替换保护，压缩后还原——这些"字节不能动"的内容不被压缩器误删。
//
// 设计：
//   - Protect：扫描内容，把只读区替换为 __RO_N__ 占位符，返回受保护文本
//   - Restore：把压缩结果里的占位符还原为原文
//   - MarkReadOnlySpans：返回只读区 span 列表（供审计/透明标记用）

package compression

import (
	"regexp"
	"sort"
	"strings"
)

// roPlaceholderBase 占位符基础字符：Unicode 私用区（U+E000 起）
// 用私用区字符而非字母/数字，避免被压缩器的「删停用词/删数字」规则误删占位符本身。
const roPlaceholderBase = rune(0xE000)

var (
	// 代码块：三反引号围住的整块，或单反引号内的短代码
	codeBlockRe = regexp.MustCompile("```[\\s\\S]*?```|`[^`\\n]+`")
	// URL：http/https 开头，到空白结束
	urlRe = regexp.MustCompile(`https?://[^\s"'<>]+`)
	// 绝对路径 / 相对路径
	pathRe = regexp.MustCompile(`(?:^|[\s("'])(?:/[\w@./-]+|\.{1,2}/[\w./-]+)`)
	// 命令：行首 $ 或 > 前缀的命令
	cmdRe = regexp.MustCompile(`(?m)^\s*[>$]\s*\S[^\n]*`)
	// 数字：整数/浮点/科学计数法
	numberRe = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b`)
)

// roSpan 内部只读区记录
type roSpan struct {
	start, end int
	kind       string
	text       string
}

// ReadOnlyProtector 只读区保护器（占位符替换 + 还原）
type ReadOnlyProtector struct {
	mapping map[string]string // 占位符 -> 原文
	spans   []Span
}

// NewReadOnlyProtector 创建保护器
func NewReadOnlyProtector() *ReadOnlyProtector {
	return &ReadOnlyProtector{mapping: make(map[string]string)}
}

// Protect 把内容中的只读区替换为占位符，返回受保护文本
func (p *ReadOnlyProtector) Protect(content string) string {
	var raw []roSpan
	collect := func(re *regexp.Regexp, kind string) {
		for _, loc := range re.FindAllStringIndex(content, -1) {
			raw = append(raw, roSpan{start: loc[0], end: loc[1], kind: kind, text: content[loc[0]:loc[1]]})
		}
	}
	collect(codeBlockRe, "code")
	collect(urlRe, "url")
	collect(pathRe, "path")
	collect(cmdRe, "command")
	collect(numberRe, "number")

	// 按 start 排序，去掉与前一个重叠的 span（先到先得，代码块优先）
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].start != raw[j].start {
			return raw[i].start < raw[j].start
		}
		return raw[i].end > raw[j].end
	})

	var kept []roSpan
	lastEnd := -1
	for _, s := range raw {
		if s.start < lastEnd {
			continue // 重叠，跳过
		}
		kept = append(kept, s)
		lastEnd = s.end
	}

	// 从后往前替换为占位符（私用区字符，不会被压缩器的删词/删数字规则误伤）
	out := content
	for i := len(kept) - 1; i >= 0; i-- {
		s := kept[i]
		token := string(roPlaceholderBase + rune(i))
		p.mapping[token] = s.text
		p.spans = append([]Span{{Start: s.start, End: s.end, Kind: s.kind}}, p.spans...)
		out = out[:s.start] + token + out[s.end:]
	}
	return out
}

// Restore 把受保护文本中的占位符还原为原文
func (p *ReadOnlyProtector) Restore(content string) string {
	out := content
	for token, original := range p.mapping {
		out = strings.ReplaceAll(out, token, original)
	}
	return out
}

// Spans 返回识别到的只读区 span 列表
func (p *ReadOnlyProtector) Spans() []Span {
	return p.spans
}

// MarkReadOnlySpans 便捷函数：只返回 span 列表（供审计）
func MarkReadOnlySpans(content string) []Span {
	p := NewReadOnlyProtector()
	p.Protect(content)
	return p.Spans()
}
