// service/compression/rtk_test.go
// 测试 RTK 工具结果压缩。

package compression

import (
	"context"
	"strings"
	"testing"
)

// 测试1：日志压缩 —— 保留 ERROR，删 INFO
func TestRTKCompressLog(t *testing.T) {
	r := NewRTKCompressor()
	in := &Input{
		Role:   RoleTool,
		Region: RegionHot,
		Content: "INFO: build started\n" +
			"ERROR: module B failed to compile\n" +
			"WARNING: deprecated API\n" +
			"INFO: done",
	}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if !strings.Contains(res, "ERROR: module B failed") {
		t.Errorf("应保留 ERROR 行: %q", res)
	}
	if strings.Contains(res, "INFO: build started") {
		t.Errorf("应删除 INFO 行: %q", res)
	}
	t.Logf("日志压缩结果: %q", res)
}

// 测试2：JSON 压缩 —— key 保留，去空白
func TestRTKCompressJSON(t *testing.T) {
	r := NewRTKCompressor()
	in := &Input{
		Role:    RoleTool,
		Region:  RegionHot,
		Content: "{\n  \"name\": \"x\",\n  \"value\": 1\n}",
	}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if !strings.Contains(res, `"name"`) || !strings.Contains(res, `"value"`) {
		t.Errorf("JSON key 不能删: %q", res)
	}
	t.Logf("JSON 压缩结果: %q", res)
}

// 测试3：普通中文不压缩（保护用户内容）
func TestRTKUnknownNotCompressed(t *testing.T) {
	r := NewRTKCompressor()
	in := &Input{
		Role:    RoleTool,
		Region:  RegionHot,
		Content: "这段普通文本不应该被压缩",
	}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if out.Content != in.Content {
		t.Errorf("未知类型不应压缩: got %q", out.Content)
	}
	t.Log("未知类型不压缩 正确")
}

// 测试4: grep压缩 —— 保留匹配行，删其他
func TestRTKCompressGrep(t *testing.T) {
	r := NewRTKCompressor()
	in := &Input{
		Role:    RoleTool,
		Region:  RegionHot,
		Content: "Info: start\nError: failed\nInfo: done",
	}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if !strings.Contains(res, "Error: failed") {
		t.Errorf("应保留匹配行: %q", res)
	}
	if strings.Contains(res, "Info: start") {
		t.Errorf("应删除非匹配行: %q", res)
	}
	t.Logf("Grep 压缩结果: %q", res)
}