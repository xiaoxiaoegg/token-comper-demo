// service/compression/prompt_test.go
// 测试系统 prompt 优化（Caveman 规则压缩）。

package compression

import (
	"context"
	"strings"
	"testing"
)

func TestPromptCompressorZhFiller(t *testing.T) {
	p := NewPromptCompressor()
	in := &Input{
		Role:    RoleSystem,
		Content: "你好，请你帮我分析这段代码，谢谢。",
	}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	// 删掉了 你好 / 请 / 谢谢
	if strings.Contains(res, "你好") {
		t.Errorf("应删除'你好': %q", res)
	}
	if strings.Contains(res, "请你") {
		t.Errorf("应删除'请你': %q", res)
	}
	if strings.Contains(res, "谢谢") {
		t.Errorf("应删除'谢谢': %q", res)
	}
	t.Logf("压缩结果: %q (原%d→新%d)", res, out.Metrics.OriginalTokens, out.Metrics.CompressedTokens)
}

func TestPromptCompressorEnStructural(t *testing.T) {
	p := NewPromptCompressor()
	in := &Input{
		Role:    RoleSystem,
		Content: "You must make sure to check the code in order to fix it.",
	}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if strings.Contains(res, "make sure to") {
		t.Errorf("应替换 make sure to: %q", res)
	}
	if strings.Contains(res, "in order to") {
		t.Errorf("应替换 in order to: %q", res)
	}
	t.Logf("压缩结果: %q", res)
}

func TestPromptCompressorNotUserContent(t *testing.T) {
	p := NewPromptCompressor()
	// 用户消息不处理（只处理 system）
	in := &Input{
		Role:    RoleUser,
		Content: "你好，请你帮我看看",
	}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if out.Content != in.Content {
		t.Errorf("用户内容不应被压缩: got %q", out.Content)
	}
	t.Log("用户内容未被压缩（正确，只处理 system）")
}

func TestPromptCompressorIntensity(t *testing.T) {
	// lite 强度：只删客套，不删结构短语
	p := NewPromptCompressor().WithIntensity(IntensityLite)
	in := &Input{
		Role:    RoleSystem,
		Content: "请务必 in order to 完成",
	}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if strings.Contains(res, "请务必") {
		t.Errorf("lite 应删客套'请务必': %q", res)
	}
	// full 强度：结构短语也删
	p2 := NewPromptCompressor().WithIntensity(IntensityFull)
	in2 := &Input{
		Role:    RoleSystem,
		Content: "请务必 in order to 完成",
	}
	out2, _ := p2.Apply(context.Background(), in2)
	res2, _ := out2.Content.(string)
	if strings.Contains(res2, "in order to") {
		t.Errorf("full 应替换 in order to: %q", res2)
	}
	t.Logf("lite→%q | full→%q", res, res2)
}
