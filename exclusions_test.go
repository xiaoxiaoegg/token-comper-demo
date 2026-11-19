package main

import (
	"testing"
)

// resetExclusions 清空全局排除规则（测试隔离用）
func resetExclusions() {
	exclusionMu.Lock()
	exclusions = nil
	exclusionMu.Unlock()
}

func TestMatchExclusion_ModelSubstringCaseInsensitive(t *testing.T) {
	resetExclusions()
	exclusions = []Exclusion{
		{ID: "e1", Type: "model", Pattern: "DouBao", Action: "skip"},
	}

	ex := matchExclusion("doubao-seed-code", "")
	if ex == nil || ex.ID != "e1" {
		t.Fatalf("期望命中 e1（大小写不敏感），实际 %+v", ex)
	}

	if matchExclusion("gpt-4", "") != nil {
		t.Errorf("gpt-4 不应命中 doubao 规则")
	}
}

func TestMatchExclusion_Endpoint(t *testing.T) {
	resetExclusions()
	exclusions = []Exclusion{
		{ID: "e2", Type: "endpoint", Pattern: "volces.com", Action: "lossless"},
	}

	ex := matchExclusion("some-model", "https://ark.cn-beijing.volces.com/api/coding/v3")
	if ex == nil || ex.Action != "lossless" {
		t.Fatalf("期望命中 endpoint 规则 e2，实际 %+v", ex)
	}

	if matchExclusion("some-model", "https://api.openai.com") != nil {
		t.Errorf("openai endpoint 不应命中 volces.com 规则")
	}
}

func TestApplyExclusions_Skip(t *testing.T) {
	resetExclusions()
	exclusions = []Exclusion{{ID: "e3", Type: "model", Pattern: "gpt", Action: "skip"}}

	plugins := []PluginMeta{{Name: "caveman"}, {Name: "rtk", Lossy: true}}
	got, ex := applyExclusions(plugins, "gpt-4", "")
	if ex == nil {
		t.Fatal("期望命中排除规则")
	}
	if len(got) != 0 {
		t.Errorf("skip 应清空插件列表，实际 %v", namesOf(got))
	}
}

func TestApplyExclusions_Lossless(t *testing.T) {
	resetExclusions()
	exclusions = []Exclusion{{ID: "e4", Type: "model", Pattern: "claude", Action: "lossless"}}

	plugins := []PluginMeta{
		{Name: "caveman"},
		{Name: "normalize"},
		{Name: "rtk", Lossy: true},
		{Name: "llmlingua", Lossy: true},
	}
	got, ex := applyExclusions(plugins, "claude-3", "")
	if ex == nil {
		t.Fatal("期望命中排除规则")
	}
	for _, p := range got {
		if p.Lossy {
			t.Errorf("lossless 应过滤有损引擎，但保留了 %s", p.Name)
		}
	}
	if len(got) != 2 {
		t.Errorf("应保留 2 个无损引擎，实际 %v", namesOf(got))
	}
}

func TestApplyExclusions_NoMatch(t *testing.T) {
	resetExclusions()
	plugins := []PluginMeta{{Name: "caveman"}, {Name: "rtk", Lossy: true}}
	got, ex := applyExclusions(plugins, "unmatched-model", "")
	if ex != nil {
		t.Fatal("不应命中排除规则")
	}
	if len(got) != 2 {
		t.Errorf("无匹配应原样返回，实际 %v", namesOf(got))
	}
}
