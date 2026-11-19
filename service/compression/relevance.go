// service/compression/relevance.go
// Relevance 查询相关性：对应计划 D8 第②部分。
//
// 按最后一条用户查询，对历史消息里的每个句子做 Jaccard 词集评分
// （减去填充词占比惩罚），只保留相关句，且保持原顺序。
// 红线：只作用历史区（RegionArchived），不碰当前轮；无相关句时回退原文（fail-closed）。

package compression

import (
	"context"
	"regexp"
	"strings"
)

var (
	// 中文/英文句子切分
	sentenceRe = regexp.MustCompile(`[^。！？!?;；\n]+`)
	// 英文单词
	enWordRe = regexp.MustCompile(`[a-z0-9]+`)
	// 中文连续段
	zhSegRe = regexp.MustCompile(`[\p{Han}]+`)
)

// 填充词（停用词）：Jaccard 评分时剔除，避免虚高
var fillerWords = map[string]bool{
	// 英文
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "for": true, "on": true, "at": true, "with": true,
	"by": true, "as": true, "it": true, "this": true, "that": true, "you": true,
	"i": true, "we": true, "they": true, "he": true, "she": true,
	// 中文
	"的": true, "了": true, "是": true, "在": true, "有": true, "和": true,
	"与": true, "就": true, "都": true, "很": true, "也": true, "还": true,
	"等": true, "这": true, "那": true, "我": true, "你": true, "他": true,
	"她": true, "它": true, "们": true, "吗": true, "呢": true, "吧": true,
	"啊": true, "哦": true, "一": true, "个": true, "不": true,
}

// RelevanceCompressor 历史相关性压缩器（低风险有损，S2）
type RelevanceCompressor struct {
	threshold float64 // 保留句的 Jaccard 阈值
}

// NewRelevanceCompressor 创建相关性压缩器
func NewRelevanceCompressor() *RelevanceCompressor {
	return &RelevanceCompressor{threshold: 0.0} // 默认保留与 query 有任一 token 重叠的句
}

func (r *RelevanceCompressor) Name() string          { return "relevance" }
func (r *RelevanceCompressor) Stage() Stage           { return S2 }
func (r *RelevanceCompressor) Mode() Mode             { return FailClosed }
func (r *RelevanceCompressor) OutputKind() OutputKind { return KindProse }

// IsApplicable 只处理历史区的消息列表，且需要最后用户查询
func (r *RelevanceCompressor) IsApplicable(in *Input) bool {
	if in == nil || in.Region != RegionArchived || in.Query == "" {
		return false
	}
	_, ok := in.Content.([]string)
	return ok
}

// Apply 对历史消息逐句评分，只保留相关句
func (r *RelevanceCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	msgs, ok := in.Content.([]string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	queryTokens := tokenize(in.Query)
	if len(queryTokens) == 0 {
		return FailOpenOutput(in), nil
	}

	originalChars := 0
	keptChars := 0
	removed := 0

	result := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		originalChars += len([]rune(msg))
		sentences := sentenceRe.FindAllString(msg, -1)
		if len(sentences) <= 1 {
			// 单句消息：不评分过滤（保护短消息）
			result = append(result, msg)
			keptChars += len([]rune(msg))
			continue
		}

		var kept []string
		for _, s := range sentences {
			score := scoreSentence(queryTokens, s)
			if score > r.threshold {
				kept = append(kept, s)
			} else {
				removed++
			}
		}
		// 全被删了 → 保留原句（保护，防止破坏语义）
		if len(kept) == 0 {
			kept = sentences
		}
		joined := strings.Join(kept, "")
		result = append(result, joined)
		keptChars += len([]rune(joined))
	}

	// 没删掉任何东西 → 返回原文（fail-open）
	if removed == 0 {
		return FailOpenOutput(in), nil
	}

	saved := max0(originalChars - keptChars)
	out := &Output{
		Content:     result,
		SavedTokens: saved,
		OutputKind:  KindProse,
		Elided:      []Elision{{Kind: "turns", Count: removed, Reason: "relevance: sentence unrelated to last query"}},
		Metrics: Metrics{
			EngineName:       r.Name(),
			OriginalTokens:   originalChars,
			CompressedTokens: keptChars,
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(result)
	return out, nil
}

// tokenize 把文本切为词集（去填充词），中文用 2-gram 捕捉词汇重叠
func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	lower := strings.ToLower(text)

	for _, w := range enWordRe.FindAllString(lower, -1) {
		if !fillerWords[w] {
			tokens[w] = true
		}
	}
	for _, seg := range zhSegRe.FindAllString(lower, -1) {
		runes := []rune(seg)
		if len(runes) == 1 {
			if !fillerWords[seg] {
				tokens[seg] = true
			}
			continue
		}
		for i := 0; i+1 < len(runes); i++ {
			tokens[string(runes[i:i+2])] = true
		}
	}
	return tokens
}

// scoreSentence 计算句子与查询的相关性：Jaccard − 填充词占比惩罚
func scoreSentence(queryTokens map[string]bool, sentence string) float64 {
	sTokens := tokenize(sentence)
	if len(sTokens) == 0 {
		return 0
	}

	inter, union := 0, 0
	for k := range queryTokens {
		if sTokens[k] {
			inter++
		}
		union++
	}
	for k := range sTokens {
		if !queryTokens[k] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	jaccard := float64(inter) / float64(union)

	// 填充词占比惩罚：句中填充词越多，越可能是套话
	fillerCount := 0
	for k := range sTokens {
		if fillerWords[k] {
			fillerCount++
		}
	}
	fillerRatio := float64(fillerCount) / float64(len(sTokens))

	return jaccard - 0.2*fillerRatio
}
