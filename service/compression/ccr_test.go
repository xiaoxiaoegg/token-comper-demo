package compression

import (
	"context"
	"strings"
	"testing"
)

func TestCCRArchiveAndExpand(t *testing.T) {
	store := NewCCRStore()
	c := NewCCRCompressor(store)

	long := strings.Repeat("历史内容", 1000) // 4000 字符，超过阈值
	in := &Input{Region: RegionArchived, Role: RoleAssistant, Content: long}

	if !c.IsApplicable(in) {
		t.Fatal("超长历史内容应可归档")
	}
	out, err := c.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	placeholder := out.Content.(string)
	if !strings.HasPrefix(placeholder, "[ccr hash=") {
		t.Fatalf("期望占位符，得到 %q", placeholder)
	}
	if out.SavedTokens <= 0 {
		t.Fatalf("期望有节省，得到 %d", out.SavedTokens)
	}

	// 展开应还原原文
	expanded := store.Expand(placeholder)
	if expanded != long {
		t.Fatal("Expand 应还原原文")
	}
}

func TestCCRShortContentSkipped(t *testing.T) {
	store := NewCCRStore()
	c := NewCCRCompressor(store)
	in := &Input{Region: RegionArchived, Role: RoleAssistant, Content: "短内容"}
	if c.IsApplicable(in) {
		t.Fatal("短内容不应归档")
	}
}

func TestCCRHotRegionSkipped(t *testing.T) {
	store := NewCCRStore()
	c := NewCCRCompressor(store)
	in := &Input{Region: RegionHot, Role: RoleTool, Content: strings.Repeat("x", 3000)}
	if c.IsApplicable(in) {
		t.Fatal("热区不应归档（红线：不碰当前轮）")
	}
}
