package middleware
// 内置了一个内存版的 Cache Store（后续生产环境可替换为 Redis 接口）。
import (
    "bytes"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "io"
    "net/http"
    "sync"

    "github.com/gin-gonic/gin"
)

// ============ 缓存存储接口 (生产环境替换为 Redis) ============

type CacheStore interface {
    Get(key string) ([]byte, bool)
    Set(key string, value []byte)
}

// MemoryCache 进程内缓存，用于 Demo 验证
type MemoryCache struct {
    mu    sync.RWMutex
    store map[string][]byte
}

func NewMemoryCache() *MemoryCache {
    return &MemoryCache{store: make(map[string][]byte)}
}

func (m *MemoryCache) Get(key string) ([]byte, bool) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    val, ok := m.store[key]
    return val, ok
}

func (m *MemoryCache) Set(key string, value []byte) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.store[key] = value
}

// ============ 精确缓存中间件 ============

var cacheStore CacheStore

func InitCacheStore(store CacheStore) {
    cacheStore = store
}

// ExactCache 精确匹配缓存中间件
func ExactCache() gin.HandlerFunc {
    return func(c *gin.Context) {
        // 1. 读取请求体
        raw, err := c.GetRawData()
        if err != nil || len(raw) == 0 {
            c.Next()
            return
        }

        var body map[string]any
        if err := json.Unmarshal(raw, &body); err != nil {
            c.Request.Body = io.NopCloser(bytes.NewReader(raw))
            c.Next()
            return
        }

        // 2. 判断是否可缓存
        // 红线：temperature>0 无 seed 不缓存；流式暂不缓存(复杂度高，后置处理)
        if !isCacheable(body) {
            c.Request.Body = io.NopCloser(bytes.NewReader(raw))
            c.Next()
            return
        }

        // 3. 规范化 JSON Key 排序，提高命中率 (Go 的 json.Marshal 对 map 自动按 key 字母序)
        canonicalRaw, _ := json.Marshal(body)
        cacheKey := generateCacheKey(canonicalRaw)

        // 4. 查缓存，命中直接返回
        if cachedResp, ok := cacheStore.Get(cacheKey); ok {
            c.Header("X-Cache-Hit", "true")
            c.Data(http.StatusOK, "application/json", cachedResp)
            c.Abort()
            return
        }

        // 5. 未命中，放行请求，并重写请求体
        c.Request.Body = io.NopCloser(bytes.NewReader(canonicalRaw))
        c.Request.ContentLength = int64(len(canonicalRaw))

        // 6. 捕获上游响应 (非流式)
        recorder := &responseRecorder{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
        c.Writer = recorder

        c.Next()

        // 7. 响应处理与缓存写入
        // 红线：含 tool_calls 响应不可回放，不缓存
        if shouldCacheResponse(recorder.body.Bytes()) {
            cacheStore.Set(cacheKey, recorder.body.Bytes())
        }
    }
}

// isCacheable 判断请求是否可缓存
func isCacheable(body map[string]any) bool {
    // 流式暂不缓存
    if stream, ok := body["stream"].(bool); ok && stream {
        return false
    }
    // temperature > 0 且无 seed 不缓存
    if temp, ok := body["temperature"].(float64); ok && temp > 0 {
        if _, hasSeed := body["seed"]; !hasSeed {
            return false
        }
    }
    return true
}

// shouldCacheResponse 判断响应是否可缓存
func shouldCacheResponse(respBody []byte) bool {
	var resp map[string]any
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return false
	}

	// 有上游错误 → 不缓存失败响应
	if errMsg, ok := resp["upstream_error"].(string); ok && errMsg != "" {
		return false
	}

	// 标准 OpenAI 响应：choices[0].message 含 tool_calls 不可回放
	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]any); ok {
			if msg, ok := firstChoice["message"].(map[string]any); ok {
				if _, hasToolCalls := msg["tool_calls"]; hasToolCalls {
					return false
				}
			}
		}
		return true
	}

	// 自定义 compressResponse（本 demo /compress 的返回）：有 messages 字段即视为可缓存
	if _, ok := resp["messages"]; ok {
		return true
	}

	return false
}

// generateCacheKey 生成 SHA256 缓存 Key
func generateCacheKey(canonicalBody []byte) string {
    h := sha256.Sum256(canonicalBody)
    return "exact_cache:" + hex.EncodeToString(h[:])
}

// ============ 响应录制器 (用于非流式缓存捕获) ============

type responseRecorder struct {
    gin.ResponseWriter
    body *bytes.Buffer
}

func (r *responseRecorder) Write(b []byte) (int, error) {
    r.body.Write(b)
    return r.ResponseWriter.Write(b)
}