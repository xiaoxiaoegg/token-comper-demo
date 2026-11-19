package compression

import (
    "regexp"
    "strings"
)

// PartitionedMessages 包含切分后的三个区
type PartitionedMessages struct {
    Frozen   []map[string]any // 冻结区：system 消息
    Archived []map[string]any // 封存区：历史轮次
    Hot      []map[string]any // 热区：当前轮/工具输出
}

// Partitioner 负责将平铺的 messages 切分为三区，并注入缓存断点
type Partitioner struct {
    enableCacheControl bool
}

func NewPartitioner(enableCacheControl bool) *Partitioner {
    return &Partitioner{enableCacheControl: enableCacheControl}
}

// Partition 执行分区逻辑
// 注意：tools 是顶级字段，天然属于前缀缓存区，不需要塞入 messages 中
func (p *Partitioner) Partition(messages []map[string]any) PartitionedMessages {
    var frozen, archived, hot []map[string]any
    msgIdx := 0

    // 1. 提取冻结区：开头的连续 System 消息
    for i, msg := range messages {
        if role, _ := msg["role"].(string); role == "system" {
            // 红线：动态值后置，保证前缀逐字节稳定
            msg = p.sweepDynamicValues(msg)
            frozen = append(frozen, msg)
        } else {
            msgIdx = i
            break
        }
    }
    
    // 边界处理：如果全是 system
    if msgIdx == 0 && len(frozen) == len(messages) {
        return PartitionedMessages{Frozen: frozen}
    }

    remainingMsgs := messages[msgIdx:]

    // 2. 切分热区与封存区
    // 热区定义为：最后一条 user 消息及其后的所有消息（包含 tool 结果）
    hotStartIdx := len(remainingMsgs)
    for i := len(remainingMsgs) - 1; i >= 0; i-- {
        if role, _ := remainingMsgs[i]["role"].(string); role == "user" {
            hotStartIdx = i
            break
        }
    }

    if hotStartIdx > 0 {
        archived = remainingMsgs[:hotStartIdx]
        hot = remainingMsgs[hotStartIdx:]
    } else {
        // 如果没有 user，或者第一条就是 user，全部当作热区
        hot = remainingMsgs
    }

    // 3. 变现：注入缓存断点 (借鉴 Anthropic Prompt Caching)
    if p.enableCacheControl {
        if len(frozen) > 0 {
            p.injectCacheControl(&frozen[len(frozen)-1])
        }
        if len(archived) > 0 {
            p.injectCacheControl(&archived[len(archived)-1])
        }
    }

    return PartitionedMessages{
        Frozen:   frozen,
        Archived: archived,
        Hot:      hot,
    }
}

// sweepDynamicValues 将动态值 (如 datetime, session_id) 移到 prompt 后缀
var dynamicPattern = regexp.MustCompile(`\{(?:datetime|date|timestamp|session_id|request_id)\}`)

func (p *Partitioner) sweepDynamicValues(msg map[string]any) map[string]any {
    content, ok := msg["content"].(string)
    if !ok {
        return msg
    }

    dynamicParts := dynamicPattern.FindAllString(content, -1)
    if len(dynamicParts) == 0 {
        return msg
    }

    cleanedContent := dynamicPattern.ReplaceAllString(content, "[DYNAMIC]")
    cleanedContent = strings.TrimSpace(cleanedContent) + "\n\nContext:\n"
    for _, part := range dynamicParts {
        cleanedContent += part + "\n"
    }

    msg["content"] = cleanedContent
    return msg
}

// injectCacheControl 在消息上注入 Anthropic 风格的 cache_control 断点
func (p *Partitioner) injectCacheControl(msg *map[string]any) {
    // 场景 1：如果是字符串 content，转为多模态数组格式以附加 cache_control
    if contentStr, ok := (*msg)["content"].(string); ok {
        (*msg)["content"] = []any{
            map[string]any{
                "type":          "text",
                "text":          contentStr,
                "cache_control": map[string]any{"type": "ephemeral"},
            },
        }
        return
    }

    // 场景 2：如果已经是数组格式，兼容 []any (json.Unmarshal 默认产物) 和 []map[string]any
    var contentArr []map[string]any
    
    switch arr := (*msg)["content"].(type) {
    case []any:
        // 将 []any 安全转换为 []map[string]any
        contentArr = make([]map[string]any, len(arr))
        converted := true
        for i, v := range arr {
            if m, ok := v.(map[string]any); ok {
                contentArr[i] = m
            } else {
                converted = false
                break
            }
        }
        if !converted || len(contentArr) == 0 {
            return
        }
        (*msg)["content"] = contentArr // 回写转换后的类型
    case []map[string]any:
        contentArr = arr
    default:
        return
    }

    // 在最后一个元素打上断点
    lastIdx := len(contentArr) - 1
    contentArr[lastIdx]["cache_control"] = map[string]any{"type": "ephemeral"}
}