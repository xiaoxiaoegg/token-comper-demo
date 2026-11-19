// service/compression/rtk_strategy_test.go
// 测试 C6 策略分派：fileContent / shellOutput / json 细化。

package compression

import (
	"context"
	"strings"
	"testing"
)

// fileContent：保前20后5 + 省略标记
func TestRTKFileContent(t *testing.T) {
	r := NewRTKCompressor()
	// 构造 30 行代码（用 func 关键字触发 codePattern）
	lines := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		lines = append(lines, "func foo() { line ")
	}
	content := strings.Join(lines, "\n")
	if detectType(content) != TypeFileContent {
		t.Fatalf("应识别为 fileContent")
	}
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if !strings.Contains(res, "[5 lines elided]") {
		t.Errorf("应含省略标记: %q", res)
	}
	t.Logf("fileContent 压缩结果长度 %d → %d", len(content), len(res))
}

// shellOutput：剥 ANSI 颜色码
func TestRTKShellAnsi(t *testing.T) {
	r := NewRTKCompressor()
	content := "\x1b[31mERROR\x1b[0m: something happened\n\x1b[32mOK\x1b[0m"
	if detectType(content) != TypeShell {
		t.Fatalf("应识别为 shell 输出")
	}
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if strings.Contains(res, "\x1b[") {
		t.Errorf("应剥掉 ANSI 颜色码: %q", res)
	}
	if !strings.Contains(res, "ERROR") {
		t.Errorf("应保留文字内容: %q", res)
	}
	t.Logf("shell 剥色结果: %q", res)
}

// json 细化：长数组保前5后2 + total
func TestRTKJSONArraySummary(t *testing.T) {
	r := NewRTKCompressor()
	// 构造 100 项长数组（>2000 字符触发细化）
	arr := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		arr = append(arr, `{"id":`+itoa(i)+`,"name":"item"}`)
	}
	content := "[" + strings.Join(arr, ",") + "]"
	if len(content) < 2000 {
		t.Fatalf("测试数据应 >2000 字符，实际 %d", len(content))
	}
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	res, _ := out.Content.(string)
	if !strings.Contains(res, `"total":100`) {
		t.Errorf("应含 total:100: %q", res)
	}
	if strings.Contains(res, `"id":99`) && len(res) > len(content) {
		t.Errorf("长数组应被摘要压缩")
	}
	t.Logf("json 数组摘要: %s", res)
}

// 辅助：整数转字符串（避免依赖 strconv）
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
