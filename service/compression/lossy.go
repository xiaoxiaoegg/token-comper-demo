// service/compression/lossy.go
// LLMLingua 近似：有损语义压缩（token 级删除低信息词）。
//
// 真实 LLMLingua 用小模型逐 token 评分重要性、删低分 token；这里用启发式规则近似：
//   - 高信息 token 保留：数字、URL/代码块（只读区保护）、专名、关键词
//   - 低信息 token 删除：停用词 / 语气词 / 冗余修饰词（英文 word 级 + 中文单字级）
//   - 每段保头尾（首尾各 3 token 必留），避免砍掉句首主语 / 句尾结论
//
// Stage = S4（高风险有损），必须由 Fidelity Gate 兜底（代码块/数字存活率校验）。

package compression

import (
	"context"
	"strings"
	"unicode"
)

// lowInfoWords 低信息词表（停用词 + 语气词 + 冗余修饰词）
var lowInfoWords = map[string]bool{
	// 英文停用词 / 冗余修饰
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"of": true, "to": true, "in": true, "on": true, "at": true, "for": true,
	"with": true, "by": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true, "could": true,
	"should": true, "may": true, "might": true, "can": true, "just": true, "really": true,
	"very": true, "quite": true, "rather": true, "somewhat": true, "actually": true,
	"basically": true, "literally": true, "simply": true, "that": true, "this": true,
	"these": true, "those": true, "it": true, "its": true, "i": true, "you": true,
	"he": true, "she": true, "we": true, "they": true, "as": true, "if": true,
	"so": true, "then": true, "than": true, "also": true, "about": true,
	// 中文单字停用词 / 语气词（只删单字，不破坏多字词）
	"的": true, "了": true, "是": true, "在": true, "有": true, "和": true,
	"与": true, "就": true, "都": true, "很": true, "也": true, "还": true,
	"等": true, "这": true, "那": true, "我": true, "你": true, "他": true,
	"她": true, "它": true, "们": true, "吗": true, "呢": true, "吧": true,
	"啊": true, "哦": true, "把": true, "被": true, "给": true, "从": true,
	"向": true, "对": true, "让": true, "要": true, "会": true, "能": true,
}

// LossyCompressor LLMLingua 近似的有损语义压缩器
type LossyCompressor struct{}

// NewLossyCompressor 创建有损语义压缩器
func NewLossyCompressor() *LossyCompressor { return &LossyCompressor{} }

func (l *LossyCompressor) Name() string          { return "llmlingua" }
func (l *LossyCompressor) Stage() Stage           { return S4 } // 高风险有损
func (l *LossyCompressor) Mode() Mode             { return FailClosed }
func (l *LossyCompressor) OutputKind() OutputKind { return KindProse }

// IsApplicable 处理任意字符串文本（用户/历史内容，需护栏授权有损）
func (l *LossyCompressor) IsApplicable(in *Input) bool {
	if in == nil {
		return false
	}
	_, ok := in.Content.(string)
	return ok
}

// Apply 有损语义压缩（只读区保护 + 删低信息 token + 保头尾）
func (l *LossyCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	content, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	// 只读区保护：代码块/URL/路径/命令/数字先占位，删完低信息词后还原
	protector := NewReadOnlyProtector()
	protected := protector.Protect(content)

	compressed := l.compress(protected)
	final := protector.Restore(compressed)

	if final == content {
		return FailOpenOutput(in), nil
	}

	saved := max0(len([]rune(content)) - len([]rune(final)))
	removed := countRemoved(content, final)

	out := &Output{
		Content:       final,
		SavedTokens:   saved,
		OutputKind:    KindProse,
		Deterministic: true,
		Techniques:    []string{"token-pruning", "llmlingua-approx"},
		Elided:        []Elision{{Kind: "tokens", Count: removed, Reason: "llmlingua: dropped low-information tokens"}},
		Metrics: Metrics{
			EngineName:       l.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(final)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(final)
	return out, nil
}

// compress token 级压缩：删低信息 token，保头尾
func (l *LossyCompressor) compress(s string) string {
	tokens := tokenizeLossy(s)
	if len(tokens) <= 12 { // 太短不压，避免过度删减
		return s
	}

	head, tail := 1, 2
	var kept []string
	removed := 0
	for i, t := range tokens {
		// 空格直接保留（保持可读性）
		if t == " " || t == "\n" {
			kept = append(kept, t)
			continue
		}
		// 保头尾（首 1 尾 2，避免过度保护停用词，也避免砍掉句尾结论）
		if i < head || i >= len(tokens)-tail {
			kept = append(kept, t)
			continue
		}
		// 删低信息 token
		if lowInfoWords[strings.ToLower(t)] {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	_ = removed
	return strings.Join(kept, "")
}

// tokenizeLossy 分词：英文 word / 中文单字 / 数字 / 标点 / 空格
func tokenizeLossy(s string) []string {
	var tokens []string
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch {
		case unicode.IsSpace(r):
			tokens = append(tokens, " ")
			i++
		case unicode.IsLetter(r) && r < 128:
			j := i
			for j < len(runes) && unicode.IsLetter(runes[j]) && runes[j] < 128 {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		case unicode.IsDigit(r):
			j := i
			for j < len(runes) && (unicode.IsDigit(runes[j]) || runes[j] == '.' || runes[j] == ',') {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		default:
			tokens = append(tokens, string(r))
			i++
		}
	}
	return tokens
}

// countRemoved 估算删除的 token 数（用于透明标记）
func countRemoved(original, compressed string) int {
	o := tokenizeLossy(original)
	c := tokenizeLossy(compressed)
	return max0(len(o) - len(c))
}
