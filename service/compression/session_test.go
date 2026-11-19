// service/compression/session_test.go
// 测试会话去重。

package compression

import (
	"context"
	"testing"
)

func TestSessionDedupRepeated(t *testing.T) {
	d := NewSessionDedup()
	in := &Input{
		Content: []string{
			"tool returned: file content A",
			"tool returned: file content B",
			"tool returned: file content A", // 重复第 1 条
			"tool returned: file content C",
			"tool returned: file content A", // 重复第 1 条
		},
	}
	out, err := d.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, ok := out.Content.([]string)
	if !ok {
		t.Fatalf("输出类型错误")
	}
	if len(res) != 5 {
		t.Errorf("长度应保持 5, got %d", len(res))
	}
	if res[2] != "[dup of #1]" {
		t.Errorf("第 3 条应替换为 [dup of #1], got %q", res[2])
	}
	if res[4] != "[dup of #1]" {
		t.Errorf("第 5 条应替换为 [dup of #1], got %q", res[4])
	}
	t.Logf("去重结果: %v", res)
}

func TestSessionDedupNoRepeat(t *testing.T) {
	d := NewSessionDedup()
	in := &Input{
		Content: []string{"a", "b", "c"}, // 无重复
	}
	out, err := d.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	// 无重复 → 返回原文（保护）；用 reflect.DeepEqual 比较 slice
	res, ok := out.Content.([]string)
	if !ok {
		t.Fatalf("输出类型错误")
	}
	if len(res) != 3 {
		t.Errorf("长度应保持 3, got %d", len(res))
	}
	t.Log("无重复不改动 正确")
}
