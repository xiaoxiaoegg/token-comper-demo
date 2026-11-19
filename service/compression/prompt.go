// service/compression/prompt.go
// 系统 prompt 优化（Caveman）：用结构化 JSON 规则压缩系统提示的冗余表达。
// 强度：lite（删客套/犹豫）→ full（+结构短语）→ ultra（+缩写）

package compression

import (
	"context"
	"embed"
	"encoding/json"
	"regexp"
	"sync"
)

//go:embed rules/*/*.json
var rulesFS embed.FS

// Intensity 压缩强度
type Intensity int

const (
	IntensityLite Intensity = iota // lite：删客套/犹豫词
	IntensityFull                  // full：+结构短语
	IntensityUltra                 // ultra：+缩写
)

// IntensityFromString 解析强度字符串
func IntensityFromString(s string) Intensity {
	switch s {
	case "ultra":
		return IntensityUltra
	case "full":
		return IntensityFull
	default:
		return IntensityLite
	}
}

// Rule 一条 Caveman 规则（对应 JSON 结构）
type Rule struct {
	Name         string `json:"name"`
	Pattern      string `json:"pattern"`
	Replacement  string `json:"replacement"`
	Context      string `json:"context"`      // all / user / system / assistant
	Category     string `json:"category"`     // filler / structural / dedup / terse / ultra
	MinIntensity string `json:"minIntensity"` // lite / full / ultra
	Description  string `json:"description"`

	re *regexp.Regexp // 编译后的正则（运行时缓存）
}

// RulePack 一个语言的规则包
type RulePack struct {
	Language string `json:"language"`
	Category string `json:"category"`
	Rules    []Rule `json:"rules"`
}

// PromptCompressor 系统 prompt 优化压缩器
type PromptCompressor struct {
	mu      sync.RWMutex
	rules   []Rule // 全部已编译规则
	loaded  bool
	intensity Intensity
}

// NewPromptCompressor 创建一个系统 prompt 优化器（默认 ultra 强度，最大化压缩）
func NewPromptCompressor() *PromptCompressor {
	p := &PromptCompressor{intensity: IntensityUltra}
	p.loadRules()
	return p
}

// WithIntensity 设置压缩强度
func (p *PromptCompressor) WithIntensity(i Intensity) *PromptCompressor {
	p.intensity = i
	return p
}

// loadRules 从 embed 加载所有语言规则
func (p *PromptCompressor) loadRules() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return
	}
	entries, err := rulesFS.ReadDir("rules")
	if err != nil {
		return
	}
	for _, langDir := range entries {
		if !langDir.IsDir() {
			continue
		}
		files, _ := rulesFS.ReadDir("rules/" + langDir.Name())
		for _, f := range files {
			if len(f.Name()) < 5 || f.Name()[len(f.Name())-5:] != ".json" {
				continue
			}
			data, err := rulesFS.ReadFile("rules/" + langDir.Name() + "/" + f.Name())
			if err != nil {
				continue
			}
			var pack RulePack
			if json.Unmarshal(data, &pack) != nil {
				continue
			}
			for _, r := range pack.Rules {
				re, err := regexp.Compile(r.Pattern)
				if err != nil {
					continue // 坏正则跳过（fail-closed：坏规则不生效）
				}
				r.re = re
				p.rules = append(p.rules, r)
			}
		}
	}
	p.loaded = true
}

// Name 实现 Compressor 接口
func (p *PromptCompressor) Name() string { return "prompt-caveman" }

// Stage 实现 Compressor 接口（近无损，S0）
func (p *PromptCompressor) Stage() Stage { return S0 }

// Mode 实现 Compressor 接口
func (p *PromptCompressor) Mode() Mode { return FailOpen }

// OutputKind 实现 Compressor 接口
func (p *PromptCompressor) OutputKind() OutputKind { return KindProse }

// IsApplicable 实现 Compressor 接口：只处理系统消息
func (p *PromptCompressor) IsApplicable(in *Input) bool {
	if in == nil || in.Role != RoleSystem {
		return false
	}
	_, ok := in.Content.(string)
	return ok
}

// Apply 实现 Compressor 接口：压缩系统 prompt 冗余
func (p *PromptCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	// 安全红线：只处理系统消息，绝不压缩用户主动内容
	if in == nil || in.Role != RoleSystem {
		return FailOpenOutput(in), nil
	}
	content, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	p.mu.RLock()
	rules := p.rules
	intensity := p.intensity
	p.mu.RUnlock()

	// 只读区保护（H14）：URL/路径/命令/代码块先占位，正则只压散文，压缩后还原
	protector := NewReadOnlyProtector()
	work := protector.Protect(content)

	applied := 0
	original := content
	for i := range rules {
		r := &rules[i]
		// 强度门槛：规则要求的强度 > 当前强度 → 跳过
		if !p.meetsIntensity(r.MinIntensity, intensity) {
			continue
		}
		// 作用范围：只处理 all / system 的规则（系统 prompt）
		if r.Context != "all" && r.Context != "system" {
			continue
		}
		if r.re == nil {
			continue
		}
		if r.re.MatchString(work) {
			work = r.re.ReplaceAllString(work, r.Replacement)
			applied++
		}
	}

	if applied == 0 {
		return FailOpenOutput(in), nil // 没压到，返回原文
	}

	// 还原只读区
	final := protector.Restore(work)
	saved := len([]rune(original)) - len([]rune(final))
	if saved < 0 {
		saved = 0 // 防膨胀
	}

	out := &Output{
		Content:       final,
		SavedTokens:   saved,
		OutputKind:    KindProse,
		Deterministic: true,
		Metrics: Metrics{
			EngineName:       p.Name(),
			OriginalTokens:   len([]rune(original)),
			CompressedTokens: len([]rune(final)),
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(final)
	return out, nil
}

// meetsIntensity 判断规则是否需要（规则强度 <= 当前强度才生效）
func (p *PromptCompressor) meetsIntensity(ruleMin string, cur Intensity) bool {
	return IntensityFromString(ruleMin) <= cur
}
