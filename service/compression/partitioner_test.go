package compression

import (
    "testing"
)

func TestPartitioner(t *testing.T) {
    p := NewPartitioner(true) // 开启 cache_control

    msgs := []map[string]any{
        {"role": "system", "content": "Current time: {datetime}"},
        {"role": "user", "content": "Hello"},
        {"role": "assistant", "content": "Hi"},
        {"role": "user", "content": "How are you?"},
    }

    res := p.Partition(msgs)

    // 验证 1：冻结区提取正确，且动态值后置（新签名不注入 tools 消息）
    if len(res.Frozen) != 1 {
        t.Errorf("Expected 1 frozen msg, got %d", len(res.Frozen))
    }
    // content 已被 injectCacheControl 转成多模态数组（装箱为 []interface{}）
    frozenContent, _ := res.Frozen[0]["content"].([]interface{})
    if len(frozenContent) == 0 {
        t.Fatal("Frozen content should be array")
    }
    firstBlock, _ := frozenContent[0].(map[string]any)
    sysText, _ := firstBlock["text"].(string)
    if sysText != "Current time: [DYNAMIC]\n\nContext:\n{datetime}\n" {
        t.Errorf("Dynamic value sweep failed: %s", sysText)
    }

    // 验证 2：断点注入 (修复了类型断言错误)
    frozenMsg := res.Frozen[len(res.Frozen)-1]
    contentArr, _ := frozenMsg["content"].([]interface{})
    first, _ := contentArr[0].(map[string]any)
    if cc, ok := first["cache_control"]; !ok || cc.(map[string]any)["type"] != "ephemeral" {
        t.Error("Cache control breakpoint not injected in frozen zone")
    }

    // 验证 3：热区是最后一条 user 及其后
    if len(res.Hot) != 1 || res.Hot[0]["content"] != "How are you?" {
        t.Error("Hot zone partition incorrect")
    }

    // 验证 4：封存区正确
    if len(res.Archived) != 2 { // user Hello + assistant Hi
        t.Error("Archived zone partition incorrect")
    }
}