package compression

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
)

// 多模态图片 Token 估算常量 (基于 OpenAI Vision 定价规则估算)
const (
    highDetailAvgTokens = 1700 // high 档平均消耗 1105-2500，取中值
    lowDetailTokens     = 85   // low 档固定消耗 85
)

// MultimodalDetailDowngrader 把非精细场景的 vision image_url.detail 从 "high" 降到 "low"
// 节省：high 1105–2500 token → low 85 token，~80%+ 节省
// 红线：① 仅非精细 OCR/图表场景；② 精细场景不降；③ 用户显式 high 不覆盖
type MultimodalDetailDowngrader struct{}

func NewMultimodalDetailDowngrader() *MultimodalDetailDowngrader {
    return &MultimodalDetailDowngrader{}
}

func (m *MultimodalDetailDowngrader) Name() string          { return "multimodal-detail" }
func (m *MultimodalDetailDowngrader) Stage() Stage           { return S0 }
func (m *MultimodalDetailDowngrader) Mode() Mode             { return FailOpen }
func (m *MultimodalDetailDowngrader) OutputKind() OutputKind { return KindStructured }

func (m *MultimodalDetailDowngrader) IsApplicable(in *Input) bool {
    if in.Role != RoleUser || in.Region != RegionHot {
        return false
    }
    parts, ok := in.Content.([]map[string]any)
    if !ok {
        return false
    }
    for _, p := range parts {
        if t, _ := p["type"].(string); t == "image_url" {
            return true
        }
    }
    return false
}

func (m *MultimodalDetailDowngrader) Apply(ctx context.Context, in *Input) (*Output, error) {
    parts, ok := in.Content.([]map[string]any)
    if !ok {
        return FailOpenOutput(in), nil
    }

    saved := 0
    originalTokens := 0
    modified := false

    for i, p := range parts {
        if t, _ := p["type"].(string); t != "image_url" {
            continue
        }

        imgURL, ok := p["image_url"].(map[string]any)
        if !ok {
            continue
        }

        currentDetail, _ := imgURL["detail"].(string)

        // 红线③：用户显式 high 不覆盖
        if currentDetail == "high" {
            continue
        }

        // 红线②：精细场景不降（TaskType 由 A1 中间件注入）
        if isFineGrainedTask(in.TaskType) {
            continue
        }

        // 红线①：非精细场景，只要当前不是 low，就主动设为 low
        // 捕获 auto 或 空值 的情况
        if currentDetail != "low" {
            imgURL["detail"] = "low"
            parts[i]["image_url"] = imgURL
            modified = true
            
            // 估算：如果原来是 auto/空，默认按 high 算
            originalTokens += highDetailAvgTokens
            saved += highDetailAvgTokens - lowDetailTokens
        } else {
            // 原本就是 low，没省下空间，但记录原始 token
            originalTokens += lowDetailTokens
        }
    }

    if !modified {
        return FailOpenOutput(in), nil
    }

    out := &Output{
        Content:       parts,
        SavedTokens:   saved,
        OutputKind:    KindStructured,
        Deterministic: true,
        Metrics: Metrics{
            EngineName:       m.Name(),
            SavedTokens:      saved,
            OriginalTokens:   originalTokens, // 修正：记录真实的原始估算值
            CompressedTokens: originalTokens - saved,
        },
    }
    out.Hash = hashContent(parts)
    return out, nil
}

// isFineGrainedTask 判定是否精细场景
func isFineGrainedTask(taskType string) bool {
    switch taskType {
    case "ocr", "chart_analysis", "code_screenshot", "diagram_reading":
        return true
    }
    return false
}

// hashContent 计算内容哈希，用于幂等缓存
func hashContent(v any) string {
    b, _ := json.Marshal(v)
    h := sha256.Sum256(b)
    return hex.EncodeToString(h[:])
}