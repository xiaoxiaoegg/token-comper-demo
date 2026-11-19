package compression

import (
	"context"
	"strings"
	"testing"
)

func TestGCFHomogeneousArray(t *testing.T) {
	g := NewGCFCompressor()
	in := &Input{Role: RoleTool, Region: RegionHot, Content: `[{"name":"Alice","age":30},{"name":"Bob","age":25}]`}

	if !g.IsApplicable(in) {
		t.Fatal("期望同质数组可压缩")
	}
	out, err := g.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)
	if !strings.HasPrefix(s, "## [2]:") {
		t.Fatalf("期望列式头，得到 %q", s)
	}
	if !strings.Contains(s, "Alice") || !strings.Contains(s, "Bob") {
		t.Fatalf("列式应包含值，得到 %q", s)
	}
	if out.SavedTokens <= 0 {
		t.Fatalf("期望有节省，得到 %d", out.SavedTokens)
	}
}

func TestGCFHeterogeneousArray(t *testing.T) {
	g := NewGCFCompressor()
	// 两个对象 key 不一致 → 不压
	in := &Input{Role: RoleTool, Region: RegionHot, Content: `[{"a":1},{"b":2}]`}
	if g.IsApplicable(in) {
		t.Fatal("异质数组不应可压缩")
	}
	out, _ := g.Apply(context.Background(), in)
	if out.SavedTokens != 0 {
		t.Fatalf("异质数组应返回原文，得到 SavedTokens=%d", out.SavedTokens)
	}
}

func TestGCFNestedFlatten(t *testing.T) {
	g := NewGCFCompressor()
	in := &Input{Role: RoleTool, Region: RegionHot, Content: `[{"customer":{"name":"A"},"id":1},{"customer":{"name":"B"},"id":2}]`}
	if !g.IsApplicable(in) {
		t.Fatal("嵌套同质数组应可压缩")
	}
	out, err := g.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)
	if !strings.Contains(s, "customer>name") {
		t.Fatalf("期望 flatten 路径列 customer>name，得到 %q", s)
	}
}
