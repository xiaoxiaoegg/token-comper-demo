// service/compression/summarize.go
// 两个历史压缩原子引擎，供「aggressive / ultra」复合预设调用：
//
//   - Summarizer：抽取式摘要（保留首句/末句 + 与 Query 相关的句子），本地启发式，
//     对应 OmniRoute 的 RuleBasedSummarizer（不调外部模型）。
//   - Ager：渐进老化（progressiveAging 近似），对长历史内容丢弃中段"陈旧"部分，
//     保留头部上下文 + 尾部最新意图，越老的内容压缩越狠。
//
// 二者皆为有损（S3），但属于"可信抽取"，由 Guard 兜底（fail-closed + 幂等）。

package compression

import (
	"context"
	"strings"
	"unicode"
)

// ---------- 句子切分（共享） ----------

// splitSentences 按中英文句末标点 + 换行切分，标点并入前文
func splitSentences(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var sents []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			sents = append(sents, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		cur.WriteRune(r)
		if r == '。' || r == '！' || r == '？' || r == '!' || r == '?' || r == '\n' {
			flush()
		}
	}
	flush()
	if len(sents) == 0 {
		// 没有任何句末标点：整体当一句
		return []string{s}
	}
	return sents
}

// wordSet 把文本拆成小写词集合（英文 word + 数字串），用于 Query 相关性打分
func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	var b strings.Builder
	flushWord := func() {
		w := strings.ToLower(b.String())
		if len([]rune(w)) >= 2 {
			set[w] = true
		}
		b.Reset()
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) && r < 128:
			b.WriteRune(r)
		case unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			flushWord()
		}
	}
	flushWord()
	return set
}

// overlapFraction 计算 sentence 与 query 词集合的重叠比例（query 词命中率）
func overlapFraction(sentence string, querySet map[string]bool) float64 {
	if len(querySet) == 0 {
		return 0
	}
	sWords := wordSet(sentence)
	if len(sWords) == 0 {
		return 0
	}
	hit := 0
	for w := range sWords {
		if querySet[w] {
			hit++
		}
	}
	return float64(hit) / float64(len(querySet))
}

// ---------- Summarizer：抽取式摘要 ----------

// Summarizer 抽取式摘要器（有损 S3）
type Summarizer struct{}

// NewSummarizer 创建摘要器
func NewSummarizer() *Summarizer { return &Summarizer{} }

func (s *Summarizer) Name() string          { return "summarize" }
func (s *Summarizer) Stage() Stage           { return S3 }
func (s *Summarizer) Mode() Mode             { return FailClosed }
func (s *Summarizer) OutputKind() OutputKind { return KindProse }

// IsApplicable 处理较长文本（>300 字符）且允许有损
func (s *Summarizer) IsApplicable(in *Input) bool {
	if in == nil {
		return false
	}
	text, ok := in.Content.(string)
	if !ok {
		return false
	}
	return in.AllowLossy && len([]rune(text)) > 300
}

// Apply 抽取式摘要：保首句 + 末句 + 与 Query 相关句
func (s *Summarizer) Apply(ctx context.Context, in *Input) (*Output, error) {
	text, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}
	sents := splitSentences(text)
	if len(sents) <= 3 {
		// 太短不摘要，避免误伤
		return FailOpenOutput(in), nil
	}

	querySet := wordSet(in.Query)
	var kept []string
	kept = append(kept, sents[0]) // 首句：上下文
	kept = append(kept, sents[len(sents)-1]) // 末句：最新意图

	for i := 1; i < len(sents)-1; i++ {
		// 与查询相关的句子必留
		if overlapFraction(sents[i], querySet) >= 0.34 {
			kept = append(kept, sents[i])
			continue
		}
		// 含数字 / 大写（疑似关键事实）的句子留
		if containsDigitOrUpper(sents[i]) {
			kept = append(kept, sents[i])
		}
	}

	if len(kept) >= len(sents) {
		return FailOpenOutput(in), nil
	}

	summary := strings.Join(kept, "")
	summary += " …[summary]"
	final := strings.TrimSpace(summary)
	if final == text {
		return FailOpenOutput(in), nil
	}

	saved := max0(len([]rune(text)) - len([]rune(final)))
	out := &Output{
		Content:       final,
		SavedTokens:   saved,
		OutputKind:    KindProse,
		Deterministic: true,
		Techniques:    []string{"extractive-summary", "query-relevance"},
		Elided:        []Elision{{Kind: "sentences", Count: len(sents) - len(kept), Reason: "summarize: dropped non-relevant middle sentences"}},
		Metrics: Metrics{
			EngineName:       s.Name(),
			OriginalTokens:   len([]rune(text)),
			CompressedTokens: len([]rune(final)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(final)
	return out, nil
}

// ---------- Ager：渐进老化 ----------

// Ager 渐进老化器（有损 S3）：丢弃长历史中段"陈旧"内容
type Ager struct{}

// NewAger 创建老化器
func NewAger() *Ager { return &Ager{} }

func (a *Ager) Name() string          { return "age" }
func (a *Ager) Stage() Stage           { return S3 }
func (a *Ager) Mode() Mode             { return FailClosed }
func (a *Ager) OutputKind() OutputKind { return KindProse }

// IsApplicable 处理较长文本（>400 字符）且允许有损
func (a *Ager) IsApplicable(in *Input) bool {
	if in == nil {
		return false
	}
	text, ok := in.Content.(string)
	if !ok {
		return false
	}
	return in.AllowLossy && len([]rune(text)) > 400
}

// Apply 渐进老化：保留头部 40% + 尾部 20% 句子，丢弃中段
func (a *Ager) Apply(ctx context.Context, in *Input) (*Output, error) {
	text, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}
	sents := splitSentences(text)
	if len(sents) <= 4 {
		return FailOpenOutput(in), nil
	}

	headN := (len(sents) * 40) / 100
	if headN < 1 {
		headN = 1
	}
	tailN := (len(sents) * 20) / 100
	if tailN < 1 {
		tailN = 1
	}
	if headN+tailN >= len(sents) {
		return FailOpenOutput(in), nil
	}

	var kept []string
	kept = append(kept, sents[:headN]...)
	kept = append(kept, sents[len(sents)-tailN:]...)

	aged := strings.Join(kept, "") + " …[aged]"
	final := strings.TrimSpace(aged)
	if final == text {
		return FailOpenOutput(in), nil
	}

	saved := max0(len([]rune(text)) - len([]rune(final)))
	out := &Output{
		Content:       final,
		SavedTokens:   saved,
		OutputKind:    KindProse,
		Deterministic: true,
		Techniques:    []string{"progressive-aging", "head-tail-keep"},
		Elided:        []Elision{{Kind: "sentences", Count: len(sents) - len(kept), Reason: "age: dropped stale middle content"}},
		Metrics: Metrics{
			EngineName:       a.Name(),
			OriginalTokens:   len([]rune(text)),
			CompressedTokens: len([]rune(final)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(final)
	return out, nil
}

// containsDigitOrUpper 判断文本是否含数字或大写字母（疑似关键事实）
func containsDigitOrUpper(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) || unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
