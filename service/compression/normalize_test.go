package compression

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeBasic(t *testing.T) {
	n := NewNormalizeCompressor()
	in := &Input{Role: RoleUser, Content: "你是什么模型\n\n\n\n这些多余的   空格你可以，，，，帮忙删除掉吗"}

	if !n.IsApplicable(in) {
		t.Fatal("user 字符串应可清理")
	}
	out, err := n.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)
	if strings.Contains(s, "\n\n") {
		t.Fatalf("连续空行未合并，得到 %q", s)
	}
	if strings.Contains(s, "    ") {
		t.Fatalf("连续空格未合并，得到 %q", s)
	}
	if strings.Contains(s, "，，") {
		t.Fatalf("重复标点未合并，得到 %q", s)
	}
	if out.SavedTokens <= 0 {
		t.Fatalf("期望有节省，得到 %d", out.SavedTokens)
	}
}

func TestNormalizeNoChange(t *testing.T) {
	n := NewNormalizeCompressor()
	in := &Input{Role: RoleUser, Content: "正常的一句话，没有多余格式。"}
	out, _ := n.Apply(context.Background(), in)
	if out.SavedTokens != 0 {
		t.Fatalf("无冗余应返回原文，得到 SavedTokens=%d", out.SavedTokens)
	}
}

func TestNormalizeSkipsNonUser(t *testing.T) {
	n := NewNormalizeCompressor()
	// assistant / tool / system 不清理
	for _, role := range []Role{RoleAssistant, RoleTool, RoleSystem} {
		in := &Input{Role: role, Content: "a\n\n\n\nb"}
		if n.IsApplicable(in) {
			t.Fatalf("role=%v 不应清理", role)
		}
	}
}

func TestNormalizeProtectsCodeBlock(t *testing.T) {
	n := NewNormalizeCompressor()
	// 代码块内的空行/缩进不应被清理（只读区保护）
	content := "看这个代码\n```\ndef foo():\n\n    return 1\n```\n结束"
	in := &Input{Role: RoleUser, Content: content}
	out, err := n.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)
	if !strings.Contains(s, "def foo():\n\n    return 1") {
		t.Fatalf("代码块内的空行/缩进应保留，得到 %q", s)
	}
}

func TestNormalizeEnglishPunct(t *testing.T) {
	n := NewNormalizeCompressor()
	in := &Input{Role: RoleUser, Content: "Really??  Yes!!!  done,,"}
	out, _ := n.Apply(context.Background(), in)
	s := out.Content.(string)
	if strings.Contains(s, "??") || strings.Contains(s, "!!") || strings.Contains(s, ",,") {
		t.Fatalf("英文重复标点未合并，得到 %q", s)
	}
}
