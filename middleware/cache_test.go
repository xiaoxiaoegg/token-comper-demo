package middleware

import (
    "bytes"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
)

func TestExactCache(t *testing.T) {
    store := NewMemoryCache()
    InitCacheStore(store)

    gin.SetMode(gin.TestMode)
    r := gin.New()
    r.Use(ExactCache())
    r.POST("/test", func(c *gin.Context) {
        c.JSON(http.StatusOK, gin.H{"choices": []gin.H{{"message": gin.H{"content": "response"}}}})
    })

    // 测试 1：乱序 JSON 请求体，应命中缓存
    body1 := `{"b":1, "a":2, "model":"gpt-4o"}`
    body2 := `{"a":2, "b":1, "model":"gpt-4o"}`

    req1 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body1))
    w1 := httptest.NewRecorder()
    r.ServeHTTP(w1, req1)
    if w1.Header().Get("X-Cache-Hit") == "true" {
        t.Fatal("First request should not hit cache")
    }

    req2 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body2))
    w2 := httptest.NewRecorder()
    r.ServeHTTP(w2, req2)
    if w2.Header().Get("X-Cache-Hit") != "true" {
        t.Fatal("Second request should hit cache despite different key order")
    }

    // 测试 2：不可缓存红线 (temperature > 0 无 seed)
    body3 := `{"model":"gpt-4o", "temperature": 0.7}`
    req3 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body3))
    w3 := httptest.NewRecorder()
    r.ServeHTTP(w3, req3)

    req4 := httptest.NewRequest("POST", "/test", bytes.NewBufferString(body3))
    w4 := httptest.NewRecorder()
    r.ServeHTTP(w4, req4)
    if w4.Header().Get("X-Cache-Hit") == "true" {
        t.Fatal("High temp without seed should not be cached")
    }
}