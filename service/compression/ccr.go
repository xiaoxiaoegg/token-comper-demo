// service/compression/ccr.go
// CCR（Content-Addressed Retrieval）归档取回：对应计划 D8 第①部分。
//
// 大块历史内容（工具长输出 / 旧轮次）归档到本地 store，用 [ccr hash=… chars=N] 占位，
// 模型需要原文时按 hash 取回注入——「压缩 × 延迟加载」。
// 红线：只作用历史区（RegionArchived），不碰当前轮；占位符可无损还原原文（低风险有损）。

package compression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"
)

// ccrPlaceholderRe 匹配 [ccr hash=xxx chars=N] 占位符
var ccrPlaceholderRe = regexp.MustCompile(`\[ccr hash=([0-9a-f]{16}) chars=(\d+)\]`)

// CCRStore 归档存储（内存版，生产环境替换为 Redis）
type CCRStore struct {
	mu    sync.RWMutex
	items map[string]string // hash -> 原文
}

// NewCCRStore 创建归档存储
func NewCCRStore() *CCRStore {
	return &CCRStore{items: make(map[string]string)}
}

// Put 归档原文，返回 16 位短 hash
func (s *CCRStore) Put(content string) string {
	h := sha256.Sum256([]byte(content))
	short := hex.EncodeToString(h[:8]) // 取前 8 字节 = 16 位 hex
	s.mu.Lock()
	s.items[short] = content
	s.mu.Unlock()
	return short
}

// Get 按 hash 取回原文
func (s *CCRStore) Get(hash string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[hash]
	return v, ok
}

// Expand 把文本中的 [ccr hash=… chars=N] 占位符展开回原文
func (s *CCRStore) Expand(content string) string {
	return ccrPlaceholderRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := ccrPlaceholderRe.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		if original, ok := s.Get(sub[1]); ok {
			return original
		}
		return m // 取不回就保留占位符（fail-closed）
	})
}

// CCRCompressor 归档取回压缩器（低风险有损，S2）
type CCRCompressor struct {
	store    *CCRStore
	minChars int // 超过此长度才归档
}

// NewCCRCompressor 创建归档压缩器
func NewCCRCompressor(store *CCRStore) *CCRCompressor {
	return &CCRCompressor{store: store, minChars: 2000}
}

func (c *CCRCompressor) Name() string          { return "ccr" }
func (c *CCRCompressor) Stage() Stage           { return S2 }
func (c *CCRCompressor) Mode() Mode             { return FailClosed }
func (c *CCRCompressor) OutputKind() OutputKind { return KindProse }

// IsApplicable 只处理历史区的长文本
func (c *CCRCompressor) IsApplicable(in *Input) bool {
	if in == nil || in.Region != RegionArchived {
		return false
	}
	content, ok := in.Content.(string)
	if !ok {
		return false
	}
	return len([]rune(content)) >= c.minChars
}

// Apply 归档长内容，返回占位符
func (c *CCRCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	content, ok := in.Content.(string)
	if !ok || c.store == nil {
		return FailOpenOutput(in), nil
	}

	hash := c.store.Put(content)
	placeholder := fmt.Sprintf("[ccr hash=%s chars=%d]", hash, len([]rune(content)))

	saved := max0(len([]rune(content)) - len([]rune(placeholder)))
	out := &Output{
		Content:     placeholder,
		SavedTokens: saved,
		OutputKind:  KindProse,
		Metrics: Metrics{
			EngineName:       c.Name(),
			OriginalTokens:   len([]rune(content)),
			CompressedTokens: len([]rune(placeholder)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hash
	return out, nil
}
