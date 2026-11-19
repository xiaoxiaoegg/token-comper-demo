package compression

import (
	"context"
	"testing"
)

func TestRelevanceFilterUnrelated(t *testing.T) {
	r := NewRelevanceCompressor()
	msgs := []string{
		"今天天气很好，阳光明媚。下午准备去公园散步。",
		"数据库连接超时，需要重试。",
		"数据库查询返回结果集。",
	}
	in := &Input{Region: RegionArchived, Content: msgs, Query: "数据库连接超时怎么办"}

	if !r.IsApplicable(in) {
		t.Fatal("历史区且有 query 应可评分")
	}
	out, err := r.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply 出错: %v", err)
	}
	filtered := out.Content.([]string)
	if len(filtered) != len(msgs) {
		t.Fatalf("消息数量应保持不变，得到 %d", len(filtered))
	}
	if out.SavedTokens <= 0 {
		t.Fatal("期望删掉不相关句从而有节省")
	}
}

func TestRelevanceNoQuerySkipped(t *testing.T) {
	r := NewRelevanceCompressor()
	in := &Input{Region: RegionArchived, Content: []string{"a", "b"}}
	if r.IsApplicable(in) {
		t.Fatal("无 query 不应评分")
	}
}

func TestRelevanceSingleSentenceNoRemoval(t *testing.T) {
	r := NewRelevanceCompressor()
	// 单句消息不评分过滤（保护短消息）
	msgs := []string{"数据库连接超时"}
	in := &Input{Region: RegionArchived, Content: msgs, Query: "数据库连接超时"}
	out, _ := r.Apply(context.Background(), in)
	if out.SavedTokens != 0 {
		t.Fatalf("单句消息不应被过滤，得到 SavedTokens=%d", out.SavedTokens)
	}
}
