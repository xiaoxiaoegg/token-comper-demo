// service/compression/pipeline.go
// 管道堆叠：对应计划 F11 第②部分。
//
// 把多个压缩器串成管道，对同一内容依次压缩（后一个压缩前一个的输出）。
// 节省按 1−(1−r₁)(1−r₂)… 叠加，最终以「原文长度 − 终稿长度」为准，
// 避免各引擎节省重叠导致虚高。
// 默认管道：rtk → gcf → caveman（工具结果先按类型压，再列式，最后压散文）。

package compression

import (
	"context"
	"encoding/json"
	"strings"
)

// Pipeline 压缩管道：把多个压缩器串起来
type Pipeline struct {
	name        string
	compressors []Compressor
}

// NewPipeline 创建管道
func NewPipeline(name string, comps ...Compressor) *Pipeline {
	return &Pipeline{name: name, compressors: comps}
}

func (p *Pipeline) Name() string { return "pipeline:" + p.name }

// Stage 取子压缩器的最高安全分级（保守）
func (p *Pipeline) Stage() Stage {
	max := S0
	for _, c := range p.compressors {
		if c.Stage() > max {
			max = c.Stage()
		}
	}
	return max
}

// Mode 任一子压缩器 fail-closed 则管道 fail-closed
func (p *Pipeline) Mode() Mode {
	for _, c := range p.compressors {
		if c.Mode() == FailClosed {
			return FailClosed
		}
	}
	return FailOpen
}

func (p *Pipeline) OutputKind() OutputKind {
	if len(p.compressors) == 0 {
		return KindProse
	}
	return p.compressors[len(p.compressors)-1].OutputKind()
}

// IsApplicable 任一子压缩器适用即可
func (p *Pipeline) IsApplicable(in *Input) bool {
	for _, c := range p.compressors {
		if c.IsApplicable(in) {
			return true
		}
	}
	return false
}

// Apply 依次应用每个压缩器
func (p *Pipeline) Apply(ctx context.Context, in *Input) (*Output, error) {
	if in == nil {
		return nil, ErrNilInput
	}
	originalLen := charLen(in.Content)
	cur := in
	var engineNames []string
	var totalSavedByEngine int

	for _, c := range p.compressors {
		if !c.IsApplicable(cur) {
			continue
		}
		out, err := c.Apply(ctx, cur)
		if err != nil {
			if c.Mode() == FailClosed {
				return nil, err
			}
			continue // fail-open：跳过该引擎继续
		}
		if out == nil || out.Content == nil {
			continue
		}
		engineNames = append(engineNames, c.Name())
		totalSavedByEngine += out.SavedTokens

		// 后一个压缩前一个的输出（Role/Region 保持不变）
		next := *cur
		next.Content = out.Content
		cur = &next
	}

	// 没压缩器实际生效 → 返回原文
	if len(engineNames) == 0 {
		return FailOpenOutput(in), nil
	}

	finalLen := charLen(cur.Content)
	saved := max0(originalLen - finalLen)

	out := &Output{
		Content:     cur.Content,
		SavedTokens: saved,
		OutputKind:  p.OutputKind(),
		Metrics: Metrics{
			EngineName:       p.Name(),
			OriginalTokens:   originalLen,
			CompressedTokens: finalLen,
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(cur.Content)
	_ = totalSavedByEngine
	return out, nil
}

// charLen 计算任意内容的字符长度（string 用 rune，其他用 JSON 序列化）
func charLen(v any) int {
	switch t := v.(type) {
	case string:
		return len([]rune(t))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return len([]rune(strings.TrimSpace(fmtSprint(v))))
		}
		return len(b)
	}
}

// fmtSprint 避免重复 import fmt
func fmtSprint(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
