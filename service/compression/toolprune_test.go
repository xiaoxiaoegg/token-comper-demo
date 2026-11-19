// service/compression/toolprune_test.go
// 测试工具定义裁剪。

package compression

import (
	"context"
	"testing"
)

func TestToolPruneCodeRole(t *testing.T) {
	p := NewToolPruner()
	tools := []ToolDef{
		{Type: "function", Function: ToolFunction{Name: "read_file", Description: "读取文件内容"}},
		{Type: "function", Function: ToolFunction{Name: "web_search", Description: "搜索网页"}},
		{Type: "function", Function: ToolFunction{Name: "git_status", Description: "查看 git 状态"}},
		{Type: "function", Function: ToolFunction{Name: "weather", Description: "查天气"}},
	}
	in := &Input{
		Role:     RoleSystem,
		TaskType: "code",
		Content:  tools,
	}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	kept, ok := out.Content.([]ToolDef)
	if !ok {
		t.Fatalf("输出类型错误")
	}
	// code 角色：保留 read_file、git_status，删除 web_search、weather
	if len(kept) != 2 {
		t.Errorf("应保留 2 个工具, got %d", len(kept))
	}
	for _, k := range kept {
		if k.Function.Name != "read_file" && k.Function.Name != "git_status" {
			t.Errorf("不该保留工具: %s", k.Function.Name)
		}
	}
	t.Logf("裁剪后保留: %d/%d 个工具", len(kept), len(tools))
}

func TestToolPruneUnknownRoleNoCut(t *testing.T) {
	p := NewToolPruner()
	tools := []ToolDef{
		{Type: "function", Function: ToolFunction{Name: "anything"}},
	}
	in := &Input{
		Role:     RoleSystem,
		TaskType: "unknown_role", // 没配置的角色
		Content:  tools,
	}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	// 未知角色不裁剪（保护）
	kept, _ := out.Content.([]ToolDef)
	if len(kept) != 1 {
		t.Errorf("未知角色不应裁剪, got %d", len(kept))
	}
	t.Log("未知角色不裁剪 正确")
}
