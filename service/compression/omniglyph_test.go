package compression

import (
	"context"
	"strings"
	"testing"
)

func TestOmniGlyphRendersPNG(t *testing.T) {
	o := NewOmniGlyphCompressor()
	content := strings.Repeat("这是一段很长的文本内容，用来测试文本转图片的载体切换功能。", 20)
	in := &Input{Role: RoleUser, Content: content}

	if !o.IsApplicable(in) {
		t.Fatal("长文本应可转图")
	}
	out, err := o.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	m, ok := out.Content.(map[string]any)
	if !ok {
		t.Fatalf("输出应为 map，得到 %T", out.Content)
	}
	if m["format"] != "png" {
		t.Fatalf("期望 png 格式，得到 %v", m["format"])
	}
	b64, _ := m["base64"].(string)
	if len(b64) == 0 {
		t.Fatal("应有 base64 图片数据")
	}
	if len(out.Techniques) == 0 {
		t.Fatal("应有遥测技术标记")
	}
}

func TestOmniGlyphSkipsShortText(t *testing.T) {
	o := NewOmniGlyphCompressor()
	in := &Input{Role: RoleUser, Content: "短文本"}
	if o.IsApplicable(in) {
		t.Fatal("短文本不应转图（无收益）")
	}
}
