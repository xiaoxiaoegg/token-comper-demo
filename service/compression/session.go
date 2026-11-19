// service/compression/session.go
// 会话去重：同一会话里重复出现的完整内容，后面用 [dup of #N] 替代。
// 对应计划 D7（Agent 场景）。
//
// 典型场景：工具多次返回完全相同的整块内容（如重复的日志、重复的列表），
// 模型不需要读第二遍——用标记告诉它"跟第 N 条一样"即可。

package compression

import (
	"context"
	"fmt"
)

// SessionDedup 会话去重器
type SessionDedup struct {
	// 已出现过的内容哈希 -> 第一次出现的序号
	seen map[string]int
}

// NewSessionDedup 创建会话去重器
func NewSessionDedup() *SessionDedup {
	return &SessionDedup{seen: make(map[string]int)}
}

// Name 实现 Compressor 接口
func (s *SessionDedup) Name() string { return "session-dedup" }

// Stage 实现 Compressor 接口（近无损，重复内容删除不影响语义）
func (s *SessionDedup) Stage() Stage { return S1 }

// Mode 实现 Compressor 接口
func (s *SessionDedup) Mode() Mode { return FailOpen }

// OutputKind 实现 Compressor 接口
func (s *SessionDedup) OutputKind() OutputKind { return KindStructured }

// IsApplicable 实现 Compressor 接口：内容是多条消息
func (s *SessionDedup) IsApplicable(in *Input) bool {
	if in == nil {
		return false
	}
	_, ok := in.Content.([]string)
	return ok
}

// Apply 实现 Compressor 接口：对消息列表做跨消息去重
// 输入：[]string（每条消息的内容）
// 输出：[]string（去重后，重复的替换为 [dup of #N]）
func (s *SessionDedup) Apply(ctx context.Context, in *Input) (*Output, error) {
	msgs, ok := in.Content.([]string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	// 记录这次调用里的去重（不污染全局，每次调用独立）
	localSeen := make(map[string]int, len(msgs))
	result := make([]string, 0, len(msgs))
	dupCount := 0

	for i, msg := range msgs {
		// 空消息不动
		if msg == "" {
			result = append(result, msg)
			continue
		}
		if firstIdx, exists := localSeen[msg]; exists {
			// 出现过 → 替换为 [dup of #N]（N 是第一次出现的序号）
			result = append(result, fmt.Sprintf("[dup of #%d]", firstIdx+1))
			dupCount++
		} else {
			localSeen[msg] = i
			result = append(result, msg)
		}
	}

	// 没有重复 → 返回原文（保护）
	if dupCount == 0 {
		return FailOpenOutput(in), nil
	}

	out := &Output{
		Content:     result,
		SavedTokens: dupCount,
		OutputKind:  KindStructured,
		Metrics: Metrics{
			EngineName:       s.Name(),
			OriginalTokens:   len(msgs),
			CompressedTokens: len(msgs) - dupCount,
			SavedTokens:      dupCount,
		},
	}
	out.Hash = hashContent(result)
	return out, nil
}
