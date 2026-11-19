package compression

import (
	"context"
	"strings"
	"testing"
)

func TestFidelityProtectedTokensFail(t *testing.T) {
	// 有损压缩删掉了代码块 → protected-tokens 不达标
	input := "看这个函数\n```go\nfunc foo() int { return 1 }\n```\n结束"
	output := "看这个函数 结束" // 代码块被删了
	cfg := FidelityConfig{Enabled: true, MinTokenSurvivalPct: 95, MinJsonKeyPct: 90}
	fr := checkFidelity(input, output, cfg)
	if fr.Passed {
		t.Fatalf("代码块被删应不通过，得到 passed=true")
	}
	if fr.FailedInvariant != "protected-tokens" {
		t.Fatalf("期望 protected-tokens 失败，得到 %s", fr.FailedInvariant)
	}
}

func TestFidelityPassWhenPreserved(t *testing.T) {
	// 代码块保留 → 通过
	input := "看这个函数\n```go\nfunc foo() int { return 1 }\n```\n结束"
	output := "看这个函数\n```go\nfunc foo() int { return 1 }\n```\n结束"
	cfg := FidelityConfig{Enabled: true, MinTokenSurvivalPct: 95, MinJsonKeyPct: 90}
	if !checkFidelity(input, output, cfg).Passed {
		t.Fatal("代码块保留应通过")
	}
}

func TestFidelityJsonKeysFail(t *testing.T) {
	// JSON key 删太多 → json-keys 不达标
	input := `{"name":"a","age":1,"city":"x","job":"y","hobby":"z"}`
	output := `{"name":"a"}` // 5 个 key 只剩 1 个（20% < 90%）
	cfg := FidelityConfig{Enabled: true, MinTokenSurvivalPct: 95, MinJsonKeyPct: 90}
	fr := checkFidelity(input, output, cfg)
	if fr.Passed {
		t.Fatal("JSON key 删太多应不通过")
	}
	if fr.FailedInvariant != "json-keys" {
		t.Fatalf("期望 json-keys 失败，得到 %s", fr.FailedInvariant)
	}
}

func TestFidelityNumericIntegrity(t *testing.T) {
	// 数字完整性：输出少了数字 → 不通过（需开启 checkNumericIntegrity）
	input := "版本 1.2.3 发布"
	output := "版本 发布"
	cfg := FidelityConfig{Enabled: true, CheckNumericIntegrity: true, MinTokenSurvivalPct: 95, MinJsonKeyPct: 90}
	if checkFidelity(input, output, cfg).Passed {
		t.Fatal("数字丢失且开启校验应不通过")
	}
}

func TestGuardFidelityReverts(t *testing.T) {
	// 集成测试：Guard 包裹一个有损压缩器，压缩删了代码块 → Guard 回退原文
	rtk := NewRTKCompressor()
	g := NewGuard(GuardConfig{
		Enabled: true,
		Fidelity: FidelityConfig{Enabled: true, MinTokenSurvivalPct: 95, MinJsonKeyPct: 90},
	}, rtk)

	// 日志内容含代码块（代码块里的 INFO 不该被日志压缩删掉）
	content := "ERROR broke\n```\nINFO inside code\n```"
	in := &Input{Role: RoleTool, Region: RegionHot, Content: content, AllowLossy: true}
	out, err := g.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	s, _ := out.Content.(string)
	if !strings.Contains(s, "INFO inside code") {
		t.Fatalf("代码块内容应保留，得到 %q", s)
	}
}
