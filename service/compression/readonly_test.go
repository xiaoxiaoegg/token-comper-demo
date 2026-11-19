package compression

import (
	"context"
	"strings"
	"testing"
)

func TestReadOnlyProtectorRoundTrip(t *testing.T) {
	p := NewReadOnlyProtector()
	content := "请看 https://api.example.com 这个接口，数字 12345，路径 /usr/local/bin"
	protected := p.Protect(content)

	if protected == content {
		t.Fatal("保护后内容应变化")
	}
	restored := p.Restore(protected)
	if restored != content {
		t.Fatalf("还原后应等于原文，得到 %q", restored)
	}
}

func TestReadOnlySpansDetected(t *testing.T) {
	spans := MarkReadOnlySpans("访问 https://example.com 和 /var/log 以及 42")
	if len(spans) < 2 {
		t.Fatalf("期望至少识别 URL 和路径，得到 %d 个 span", len(spans))
	}
	kinds := map[string]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	if !kinds["url"] || !kinds["path"] {
		t.Fatalf("期望识别 url 和 path 类型，得到 %v", kinds)
	}
}

func TestCavemanProtectsURL(t *testing.T) {
	p := NewPromptCompressor().WithIntensity(IntensityUltra)
	// 散文里的 "make sure to" 应被压成 "ensure"，但 URL 里的数字/内容是只读区，不该动
	content := "please make sure to visit https://example.com/docs/12345 for details."
	in := &Input{Role: RoleSystem, Content: content}
	out, err := p.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)
	if !strings.Contains(s, "https://example.com/docs/12345") {
		t.Fatalf("URL 应保持不变，得到 %q", s)
	}
	if !strings.Contains(s, "ensure") {
		t.Fatalf("散文 make sure to 应被压缩为 ensure，得到 %q", s)
	}
}
