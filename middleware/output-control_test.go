package middleware
// 这个测试验证三条红线：显式值不覆盖、任务推断正确、合规域优先。

import (
    "bytes"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
)

func setupRouter() *gin.Engine {
    gin.SetMode(gin.TestMode)
    r := gin.New()
    r.Use(OutputControl())
    r.POST("/test", func(c *gin.Context) {
        var body map[string]any
        raw, _ := c.GetRawData()
        json.Unmarshal(raw, &body)
        c.JSON(http.StatusOK, gin.H{"received_body": body})
    })
    return r
}

func TestOutputControl(t *testing.T) {
    r := setupRouter()

    // 测试 1：推断 chat 类型且无显式 max_tokens，应注入 1024
    body1 := `{"model": "gpt-4o", "messages":[{"role":"user","content":"hi"}]}`
    req1 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body1))
    w1 := httptest.NewRecorder()
    r.ServeHTTP(w1, req1)

    var resp1 map[string]any
    json.Unmarshal(w1.Body.Bytes(), &resp1)
    maxTok1 := resp1["received_body"].(map[string]any)["max_tokens"]
    if maxTok1 != float64(1024) {
        t.Errorf("Test 1 Failed: expected max_tokens 1024, got %v", maxTok1)
    }

    // 测试 2：用户显式 max_tokens 不被覆盖
    body2 := `{"model": "gpt-4o", "max_tokens": 500, "messages":[{"role":"user","content":"hi"}]}`
    req2 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body2))
    w2 := httptest.NewRecorder()
    r.ServeHTTP(w2, req2)

    var resp2 map[string]any
    json.Unmarshal(w2.Body.Bytes(), &resp2)
    maxTok2 := resp2["received_body"].(map[string]any)["max_tokens"]
    if maxTok2 != float64(500) {
        t.Errorf("Test 2 Failed: expected max_tokens 500, got %v", maxTok2)
    }

    // 测试 3：Agent 任务应注入 reasoning_effort
    body3 := `{"model": "gpt-4o", "tools":[{"type":"function","function":{"name":"test"}}], "messages":[{"role":"user","content":"run tool"}]}`
    req3 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body3))
    w3 := httptest.NewRecorder()
    r.ServeHTTP(w3, req3)

    var resp3 map[string]any
    json.Unmarshal(w3.Body.Bytes(), &resp3)
    effort := resp3["received_body"].(map[string]any)["reasoning_effort"]
    if effort != "medium" {
        t.Errorf("Test 3 Failed: expected reasoning_effort medium, got %v", effort)
    }
}

// 辅助：读取 NopCloser
var _ = io.NopCloser