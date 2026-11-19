package compression

import (
	"context"
	"strings"
	"testing"
)

func TestLossyRemovesStopwords(t *testing.T) {
	l := NewLossyCompressor()
	in := &Input{Role: RoleUser, Content: "this is a very important test case for the system now"}
	out, _ := l.Apply(context.Background(), in)
	s := out.Content.(string)
	// 删了英文停用词/修饰词
	for _, w := range []string{" is ", " a ", " very ", " for ", " the "} {
		if strings.Contains(s, w) {
			t.Fatalf("停用词 %q 应被删，得到 %q", w, s)
		}
	}
	// 保留关键词
	if !strings.Contains(s, "important") || !strings.Contains(s, "test") {
		t.Fatalf("关键词应保留，得到 %q", s)
	}
	if out.SavedTokens <= 0 {
		t.Fatalf("应有节省，得到 %d", out.SavedTokens)
	}
}

func TestLossyPreservesNumbers(t *testing.T) {
	l := NewLossyCompressor()
	in := &Input{Role: RoleUser, Content: "the version is 1.2.3 and the count is 42 items"}
	out, _ := l.Apply(context.Background(), in)
	s := out.Content.(string)
	if !strings.Contains(s, "1.2.3") || !strings.Contains(s, "42") {
		t.Fatalf("数字应保留，得到 %q", s)
	}
}

func TestLossyChineseStopwords(t *testing.T) {
	l := NewLossyCompressor()
	in := &Input{Role: RoleUser, Content: "这是一个很重要的数据库系统的测试内容"}
	out, _ := l.Apply(context.Background(), in)
	s := out.Content.(string)
	// 中文单字停用词被删（的/是/很/一/个）
	if !strings.Contains(s, "数据库") || !strings.Contains(s, "系统") {
		t.Fatalf("中文关键词应保留，得到 %q", s)
	}
}

func TestLossyGuardProtectsCodeBlock(t *testing.T) {
	l := NewLossyCompressor()
	g := NewGuard(GuardConfig{
		Enabled:  true,
		Fidelity: FidelityConfig{Enabled: true, MinTokenSurvivalPct: 95, MinJsonKeyPct: 90},
	}, l)
	content := "see the code below\n```go\nfunc main() {}\n```\nend of it"
	in := &Input{Role: RoleUser, Content: content, AllowLossy: true}
	out, _ := g.Apply(context.Background(), in)
	s, _ := out.Content.(string)
	if !strings.Contains(s, "func main") {
		t.Fatalf("代码块应保留（只读区保护 + fidelity 兜底），得到 %q", s)
	}
}
