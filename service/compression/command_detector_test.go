package compression

import (
	"context"
	"strings"
	"testing"
)

func TestDetectCommandBuildVite(t *testing.T) {
	d := detectCommand("npm run build", "")
	if d == nil {
		t.Fatal("应检测到命令类型")
	}
	if d.Type != "build-vite" {
		t.Fatalf("期望 build-vite，得到 %s", d.Type)
	}
	if d.Confidence < 0.8 {
		t.Fatalf("命令匹配置信度应高，得到 %f", d.Confidence)
	}
	if d.Category != "build" {
		t.Fatalf("期望 build 分类，得到 %s", d.Category)
	}
}

func TestDetectCommandByContent(t *testing.T) {
	// 命令为空，靠内容特征兜底
	d := detectCommand("", "vite v5.0.0 building for production...\n✓ built in 100ms")
	if d == nil || d.Type != "build-vite" {
		t.Fatalf("期望靠内容识别 build-vite，得到 %v", d)
	}
	if d.Confidence > 0.9 {
		t.Fatalf("内容兜底置信度应 < 0.9，得到 %f", d.Confidence)
	}
}

func TestRTKCommandAwareCompression(t *testing.T) {
	r := NewRTKCompressor()
	content := "vite v5.0.0 building for production...\ntransforming index.html\n✓ 10 modules transformed.\ndist/assets/index.js 12.3 kB\n✓ built in 100ms"
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content, Command: "npm run build", AllowLossy: true}

	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s := out.Content.(string)

	// 删了 transforming 行
	if strings.Contains(s, "transforming") {
		t.Fatalf("transforming 行应被删，得到 %q", s)
	}
	// 保留了构建摘要和 error
	if !strings.Contains(s, "built in") {
		t.Fatalf("built in 摘要应保留，得到 %q", s)
	}
	// 遥测字段应填充
	if len(out.Techniques) == 0 || !containsStr(out.Techniques, "command-aware") {
		t.Fatalf("应返回 command-aware 技术，得到 %v", out.Techniques)
	}
	if len(out.Rules) == 0 {
		t.Fatal("应返回规则名")
	}
	if out.Confidence <= 0 {
		t.Fatalf("应有置信度，得到 %f", out.Confidence)
	}
	if out.SavedTokens <= 0 {
		t.Fatalf("应有节省，得到 %d", out.SavedTokens)
	}
}

func TestRTKCommandAwareKeepError(t *testing.T) {
	r := NewRTKCompressor()
	content := "vite v5.0.0 building...\nERROR: something failed\ntransforming x.js\n✓ built in 50ms"
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content, Command: "vite build", AllowLossy: true}
	out, _ := r.Apply(context.Background(), in)
	s := out.Content.(string)
	if !strings.Contains(s, "ERROR: something failed") {
		t.Fatalf("error 行应保留，得到 %q", s)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
