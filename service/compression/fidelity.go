// service/compression/fidelity.go
// Fidelity Gate（保真校验）：压缩后校验「关键内容是否存活」，不达标则回退原文。
// 对齐 OmniRoute 的 fidelityGate.ts：protected-tokens / numeric / json-keys 三个不变量。
//
// 设计原则：
//   - 只校验「受保护/关键」内容（只读区 token、数字、JSON key），不校验普通散文。
//   - FAIL-OPEN：校验器自身出错绝不能阻塞压缩（返回 Passed=true）。
//   - 便宜 → 昂贵的顺序，第一个失败立即返回。

package compression

import (
	"fmt"
	"regexp"
	"strings"
)

// FidelityConfig 保真校验配置
type FidelityConfig struct {
	Enabled               bool
	MinTokenSurvivalPct   float64 // 受保护 token 存活率（%），默认 95
	MinJsonKeyPct         float64 // JSON key 存活率（%），默认 90
	CheckNumericIntegrity bool    // 数字完整性校验，默认 true
}

// FidelityResult 校验结果
type FidelityResult struct {
	Passed          bool
	FailedInvariant string // protected-tokens | numeric | json-keys
	Detail          string
}

var (
	// 数字字面量（含尾随数字/点/逗号，对齐 OmniRoute NUMERIC_RE，抗 ReDoS 有界）
	fidelityNumericRe = regexp.MustCompile(`\d[\d.,]{0,40}`)
	// JSON key
	fidelityJsonKeyRe = regexp.MustCompile(`"([A-Za-z_$][\w$-]{0,80})"\s*:`)
)

// checkFidelity 压缩后校验关键内容存活率
func checkFidelity(input, output string, cfg FidelityConfig) FidelityResult {
	if input == output {
		return FidelityResult{Passed: true}
	}

	// 1. protected-tokens：最不能丢的结构性内容（代码块 / 命令）必须存活
	//    只校验 code/command，不校验 url/path（后者常出现在会被合理删除的日志行里，易误伤）
	spans := MarkReadOnlySpans(input)
	var tokens []string
	for _, s := range spans {
		if s.Kind == "code" || s.Kind == "command" {
			c := strings.TrimSpace(input[s.Start:s.End])
			if len(c) >= 3 { // 过滤过短碎片，避免噪音
				tokens = append(tokens, c)
			}
		}
	}
	if len(tokens) > 0 {
		minTok := cfg.MinTokenSurvivalPct
		if minTok <= 0 {
			minTok = 95
		}
		survived := 0
		for _, t := range uniqueStrings(tokens) {
			if strings.Contains(output, t) {
				survived++
			}
		}
		ratio := float64(survived) / float64(len(uniqueStrings(tokens)))
		if ratio*100 < minTok {
			return FidelityResult{
				Passed:          false,
				FailedInvariant: "protected-tokens",
				Detail:          fmt.Sprintf("受保护 token 存活 %.0f%% < %.0f%%", ratio*100, minTok),
			}
		}
	}

	// 2. numeric：数字完整性
	if cfg.CheckNumericIntegrity {
		for _, num := range uniqueStrings(fidelityNumericRe.FindAllString(input, -1)) {
			if !strings.Contains(output, num) {
				return FidelityResult{
					Passed:          false,
					FailedInvariant: "numeric",
					Detail:          fmt.Sprintf("数字 %q 在输出中缺失", num),
				}
			}
		}
	}

	// 3. json-keys：JSON key 存活率
	var keys []string
	for _, m := range fidelityJsonKeyRe.FindAllStringSubmatch(input, -1) {
		if len(m) > 1 {
			keys = append(keys, m[1])
		}
	}
	keys = uniqueStrings(keys)
	if len(keys) > 0 {
		minKey := cfg.MinJsonKeyPct
		if minKey <= 0 {
			minKey = 90
		}
		survived := 0
		for _, k := range keys {
			if strings.Contains(output, `"`+k+`"`) {
				survived++
			}
		}
		ratio := float64(survived) / float64(len(keys))
		if ratio*100 < minKey {
			return FidelityResult{
				Passed:          false,
				FailedInvariant: "json-keys",
				Detail:          fmt.Sprintf("JSON key 存活 %.0f%% < %.0f%%", ratio*100, minKey),
			}
		}
	}

	return FidelityResult{Passed: true}
}

// uniqueStrings 去重（保持顺序）
func uniqueStrings(vals []string) []string {
	seen := make(map[string]bool, len(vals))
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
