package main

import (
	"testing"

	"token-comper-demo/service/compression"
)

// namesOf 提取插件名列表，方便断言
func namesOf(plugins []PluginMeta) []string {
	out := make([]string, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.Name)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestResolvePlugins_DefaultPolicy(t *testing.T) {
	policy := AdminPolicy{MaxLossy: false, DefaultPlugins: []string{"normalize", "caveman", "prefix", "tool-prune", "gcf"}}
	got := namesOf(resolvePlugins(nil, policy))
	if len(got) != 5 {
		t.Fatalf("期望默认 5 个插件，实际 %v", got)
	}
	for _, want := range []string{"normalize", "caveman", "prefix", "tool-prune", "gcf"} {
		if !contains(got, want) {
			t.Errorf("默认策略缺少 %s，实际 %v", want, got)
		}
	}
}

func TestResolvePlugins_LiteExpandsToLosslessAtoms(t *testing.T) {
	key := &KeyCompression{Plugins: []string{"lite"}}
	policy := AdminPolicy{MaxLossy: false}
	got := namesOf(resolvePlugins(key, policy))
	for _, want := range []string{"normalize", "caveman", "tool-prune", "gcf"} {
		if !contains(got, want) {
			t.Errorf("lite 应展开含 %s，实际 %v", want, got)
		}
	}
	// lite 全是无损，即使 MaxLossy=false 也应全部保留
	if contains(got, "rtk") || contains(got, "llmlingua") {
		t.Errorf("lite 不应包含有损引擎，实际 %v", got)
	}
}

func TestResolvePlugins_AggressiveLossyGated(t *testing.T) {
	// 注意有损是双层门：管理员 MaxLossy 与 key.Lossy 同时为真才放行有损
	key := &KeyCompression{Plugins: []string{"aggressive"}, Lossy: true}

	// 管理员 MaxLossy=false：有损原子全部被过滤，只剩无损子集
	gotLossless := namesOf(resolvePlugins(key, AdminPolicy{MaxLossy: false}))
	for _, lossy := range []string{"rtk", "relevance", "ccr", "summarize", "age", "llmlingua"} {
		if contains(gotLossless, lossy) {
			t.Errorf("MaxLossy=false 时 %s 不应出现，实际 %v", lossy, gotLossless)
		}
	}
	if !contains(gotLossless, "caveman") || !contains(gotLossless, "gcf") {
		t.Errorf("aggressive 的无损子集应保留 caveman/gcf，实际 %v", gotLossless)
	}

	// 管理员 MaxLossy=true + key.Lossy=true：有损原子保留
	gotLossy := namesOf(resolvePlugins(key, AdminPolicy{MaxLossy: true}))
	for _, want := range []string{"rtk", "relevance", "summarize", "age", "llmlingua", "ccr"} {
		if !contains(gotLossy, want) {
			t.Errorf("MaxLossy=true 时 aggressive 应含 %s，实际 %v", want, gotLossy)
		}
	}

	// key.Lossy=false 时即使管理员 MaxLossy=true 也过滤有损
	keyNoLossy := &KeyCompression{Plugins: []string{"aggressive"}, Lossy: false}
	gotKeyGate := namesOf(resolvePlugins(keyNoLossy, AdminPolicy{MaxLossy: true}))
	if contains(gotKeyGate, "rtk") {
		t.Errorf("key.Lossy=false 时 rtk 不应出现（双层门），实际 %v", gotKeyGate)
	}
}

func TestResolvePlugins_UltraIncludesOmniglyph(t *testing.T) {
	key := &KeyCompression{Plugins: []string{"ultra"}, Lossy: true}
	got := namesOf(resolvePlugins(key, AdminPolicy{MaxLossy: true}))
	if !contains(got, "omniglyph") {
		t.Errorf("ultra 应包含 omniglyph，实际 %v", got)
	}
}

func TestResolvePlugins_Dedupe(t *testing.T) {
	// 显式列 caveman 又用 lite（内含 caveman），应去重
	key := &KeyCompression{Plugins: []string{"caveman", "lite"}}
	got := namesOf(resolvePlugins(key, AdminPolicy{MaxLossy: false}))
	count := 0
	for _, n := range got {
		if n == "caveman" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("caveman 应去重后只出现一次，实际 %d 次：%v", count, got)
	}
}

func TestResolvePlugins_AllExpandsLosslessOnly(t *testing.T) {
	key := &KeyCompression{Plugins: []string{"all"}}
	got := resolvePlugins(key, AdminPolicy{MaxLossy: false})
	for _, p := range got {
		if p.Lossy {
			t.Errorf("all 应只展开无损插件，但包含有损 %s", p.Name)
		}
	}
	if len(got) == 0 {
		t.Errorf("all 应展开至少一个无损插件")
	}
}

func TestClampIntensity(t *testing.T) {
	cases := []struct {
		user, ceil string
		want       compression.Intensity
	}{
		{"lite", "full", compression.IntensityLite},
		{"ultra", "full", compression.IntensityFull},
		{"full", "ultra", compression.IntensityFull},
		{"full", "lite", compression.IntensityLite},
	}
	for _, c := range cases {
		got := clampIntensity(c.user, c.ceil)
		if got != c.want {
			t.Errorf("clampIntensity(%q,%q) = %v，期望 %v", c.user, c.ceil, got, c.want)
		}
	}
}
