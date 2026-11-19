// service/compression/command_detector.go
// 命令感知检测器 + 命令过滤器（对齐 OmniRoute 的 commandDetector.ts + filters/*.json）。
//
// 思路：从 assistant 的 tool_calls 解析出 shell 命令，检测命令类型（build/test/git…），
// 用该命令类型对应的过滤器（keep/drop/collapse 规则）压缩工具输出，
// 比「只看内容类型」更精准——同一个输出，知道是 `npm run build` 就只留构建错误和摘要。

package compression

import "regexp"

// matchesAny 判断行是否匹配任一正则
func matchesAny(line string, patterns []*regexp.Regexp) bool {
	for _, re := range patterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// CommandDetection 命令检测结果
type CommandDetection struct {
	Type            string   // 命令类型，如 build-vite / go-test
	Command         string   // 原始命令
	Confidence      float64  // 置信度 0~1
	Category        string   // 分类：build/test/git/shell/generic
	MatchedPatterns []string // 命中的正则（审计用）
}

// cmdDetector 一个命令类型的检测器
type cmdDetector struct {
	Type     string
	Category string
	Command  []*regexp.Regexp // 匹配命令字符串
	Content  []*regexp.Regexp // 匹配输出内容特征（命令为空时兜底）
}

// commandDetectors 内置命令检测器（精简版，对齐 OmniRoute DETECTORS 的子集）
var commandDetectors = []cmdDetector{
	{
		Type: "build-vite", Category: "build",
		Command: []*regexp.Regexp{
			regexp.MustCompile(`^vite\s+build\b`),
			regexp.MustCompile(`^npm\s+run\s+build\b`),
			regexp.MustCompile(`^pnpm\s+build\b`),
			regexp.MustCompile(`^yarn\s+build\b`),
		},
		Content: []*regexp.Regexp{
			regexp.MustCompile(`vite v[\d.]+`),
			regexp.MustCompile(`✓ built in`),
		},
	},
	{
		Type: "go-test", Category: "test",
		Command: []*regexp.Regexp{regexp.MustCompile(`^go\s+test\b`)},
		Content: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^(=== RUN|--- FAIL|--- PASS|FAIL|PASS|ok\s)`),
		},
	},
	{
		Type: "npm-test", Category: "test",
		Command: []*regexp.Regexp{regexp.MustCompile(`^(?:npm|pnpm|yarn)\s+(?:run\s+)?test\b`)},
		Content: []*regexp.Regexp{
			regexp.MustCompile(`Tests?:`),
			regexp.MustCompile(`(?m)^(PASS|FAIL)`),
		},
	},
	{
		Type: "go-build", Category: "build",
		Command: []*regexp.Regexp{regexp.MustCompile(`^go\s+build\b`)},
		Content: []*regexp.Regexp{
			regexp.MustCompile(`(?m)\.go:\d+:\d+:`),
		},
	},
	{
		Type: "git-diff", Category: "git",
		Command: []*regexp.Regexp{regexp.MustCompile(`^git\s+diff\b`)},
		Content: []*regexp.Regexp{
			regexp.MustCompile(`^diff --git`),
			regexp.MustCompile(`^@@\s+-`),
		},
	},
	{
		Type: "git-status", Category: "git",
		Command: []*regexp.Regexp{regexp.MustCompile(`^git\s+status\b`)},
		Content: []*regexp.Regexp{
			regexp.MustCompile(`^On branch`),
			regexp.MustCompile(`^(Changes|Untracked|nothing to commit)`),
		},
	},
}

// detectCommand 检测命令类型：优先命令匹配（高置信），其次内容特征兜底（低置信）
func detectCommand(command, content string) *CommandDetection {
	// 1. 命令匹配（置信度最高）
	if command != "" {
		for _, d := range commandDetectors {
			for _, re := range d.Command {
				if re.MatchString(command) {
					return &CommandDetection{
						Type: d.Type, Command: command, Confidence: 0.9,
						Category: d.Category, MatchedPatterns: []string{re.String()},
					}
				}
			}
		}
	}
	// 2. 内容特征兜底（命令为空或未命中时）
	for _, d := range commandDetectors {
		matched := 0
		var patterns []string
		for _, re := range d.Content {
			if re.MatchString(content) {
				matched++
				patterns = append(patterns, re.String())
			}
		}
		if matched > 0 && matched*2 >= len(d.Content) {
			conf := 0.5 + 0.15*float64(matched)
			if conf > 0.85 {
				conf = 0.85
			}
			return &CommandDetection{
				Type: d.Type, Command: command, Confidence: conf,
				Category: d.Category, MatchedPatterns: patterns,
			}
		}
	}
	return nil
}

// rtkFilter 命令过滤器：keep/drop/collapse 三类行规则
type rtkFilter struct {
	StripAnsi        bool
	KeepPatterns     []*regexp.Regexp // 保留匹配行（优先级最高）
	DropPatterns     []*regexp.Regexp // 删除匹配行
	CollapsePatterns []*regexp.Regexp // 折叠匹配行（合并计数）
}

// rtkFilters 命令类型 → 过滤器（对齐 OmniRoute filters/*.json 的精简版）
var rtkFilters = map[string]*rtkFilter{
	"build-vite": {
		StripAnsi: true,
		KeepPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)error|failed`),
			regexp.MustCompile(`✓ built in`),
		},
		DropPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^transforming `),
			regexp.MustCompile(`^✓ \d+ modules transformed`),
			regexp.MustCompile(`^\s*$`),
		},
		CollapsePatterns: []*regexp.Regexp{
			regexp.MustCompile(`^dist/`),
		},
	},
	"go-test": {
		KeepPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^--- FAIL|FAIL\b|(?i)error`),
		},
		DropPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^=== RUN`),
			regexp.MustCompile(`^--- PASS`),
			regexp.MustCompile(`^ok\s`),
		},
	},
	"npm-test": {
		KeepPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^FAIL|Tests?:|(?i)error|failed`),
		},
		DropPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)^PASS\s`),
			regexp.MustCompile(`^\s*$`),
		},
	},
	"go-build": {
		KeepPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?m)\.go:\d+:\d+:`),
			regexp.MustCompile(`(?i)error`),
		},
		DropPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*$`),
		},
	},
	"git-diff": {
		KeepPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^diff --git`),
			regexp.MustCompile(`^@@\s+-`),
			regexp.MustCompile(`^[+-]`),
		},
		DropPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*$`),
		},
	},
	"git-status": {
		KeepPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^On branch`),
			regexp.MustCompile(`^(Changes|Untracked|nothing to commit|modified|new file|deleted)`),
			regexp.MustCompile(`^\s+`),
		},
		DropPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^\s*$`),
		},
	},
}
