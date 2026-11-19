package compression

import (
    "context"
    "testing"
)

func TestMultimodalDetailDowngrader(t *testing.T) {
    d := NewMultimodalDetailDowngrader()

    // 场景 1：非精细任务，未指定 detail，应降为 low
    in1 := &Input{
        Role:     RoleUser,
        Region:   RegionHot,
        TaskType: "chat",
        Content: []map[string]any{
            {"type": "text", "text": "What is this?"},
            {"type": "image_url", "image_url": map[string]any{"url": "http://example.com/img.png"}},
        },
    }
    if !d.IsApplicable(in1) {
        t.Fatal("Should be applicable")
    }
    out1, _ := d.Apply(context.Background(), in1)
    parts1 := out1.Content.([]map[string]any)
    detail1 := parts1[1]["image_url"].(map[string]any)["detail"]
    if detail1 != "low" {
        t.Errorf("Test 1 Failed: expected detail low, got %v", detail1)
    }

    // 场景 2：用户显式 high，不覆盖
    in2 := &Input{
        Role:     RoleUser,
        Region:   RegionHot,
        TaskType: "chat",
        Content: []map[string]any{
            {"type": "image_url", "image_url": map[string]any{"url": "http://example.com/img.png", "detail": "high"}},
        },
    }
    out2, _ := d.Apply(context.Background(), in2)
    parts2 := out2.Content.([]map[string]any)
    detail2 := parts2[0]["image_url"].(map[string]any)["detail"]
    if detail2 != "high" {
        t.Errorf("Test 2 Failed: expected detail high, got %v", detail2)
    }

    // 场景 3：精细任务，不降级
    in3 := &Input{
        Role:     RoleUser,
        Region:   RegionHot,
        TaskType: "ocr",
        Content: []map[string]any{
            {"type": "image_url", "image_url": map[string]any{"url": "http://example.com/img.png"}},
        },
    }
    out3, _ := d.Apply(context.Background(), in3)
    if out3.SavedTokens != 0 {
        t.Errorf("Test 3 Failed: expected 0 saved tokens for OCR, got %d", out3.SavedTokens)
    }
}