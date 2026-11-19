package compression

import (
	"context"
	"strings"
	"testing"
)

func TestPipelineStacking(t *testing.T) {
	rtk := NewRTKCompressor()
	gcf := NewGCFCompressor()
	pipe := NewPipeline("rtk-gcf", rtk, gcf)

	// rtk 先紧凑 JSON，gcf 再列式编码
	content := `[{"name": "Alice", "age": 30}, {"name": "Bob", "age": 25}]`
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content, AllowLossy: true}

	out, err := pipe.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)
	if !strings.HasPrefix(s, "## [2]:") {
		t.Fatalf("期望管道最终输出 gcf 列式，得到 %q", s)
	}
	if out.SavedTokens <= 0 {
		t.Fatalf("期望有节省，得到 %d", out.SavedTokens)
	}
}

func TestPipelineNameAndStage(t *testing.T) {
	pipe := NewPipeline("rtk-gcf", NewRTKCompressor(), NewGCFCompressor())
	if pipe.Name() != "pipeline:rtk-gcf" {
		t.Fatalf("期望名称 pipeline:rtk-gcf，得到 %s", pipe.Name())
	}
	if pipe.Stage() != S2 {
		t.Fatalf("管道应取最高安全分级 S2，得到 %v", pipe.Stage())
	}
	if pipe.Mode() != FailClosed {
		t.Fatalf("含 fail-closed 子压缩器时管道应 fail-closed")
	}
}

func TestPipelineNoOpReturnsOriginal(t *testing.T) {
	// 两个压缩器都不适用 → 返回原文
	pipe := NewPipeline("x", NewRTKCompressor())
	in := &Input{Role: RoleSystem, Region: RegionFrozen, Content: "普通 system 文本"}
	out, _ := pipe.Apply(context.Background(), in)
	if out.SavedTokens != 0 {
		t.Fatalf("无压缩器生效应返回原文，得到 SavedTokens=%d", out.SavedTokens)
	}
}
