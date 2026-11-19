package compression

import (
    "sync"
    "time"
)

// MeterEntry 一次压缩的计量记录 (对齐 J16 方案)
type MeterEntry struct {
    RequestID        string    `json:"request_id"`
    EngineName       string    `json:"engine_name"`
    OriginalTokens   int       `json:"original_tokens"`
    CompressedTokens int       `json:"compressed_tokens"`
    SavedTokens      int       `json:"saved_tokens"`
    IsCached         bool      `json:"is_cached"`
    CallCost         float64   `json:"call_cost"` // 预留：压缩自身开销（如调用 LLM 压缩的成本）
    At               time.Time `json:"at"`
}

// MeterStats 统计快照，避免直接暴露内部 map
type MeterStats struct {
    TotalSaved      int            // 总节省 token
    TotalEntries    int            // 总记录条数
    CacheHits       int            // 缓存命中次数
    EngineSaved     map[string]int // 各引擎累计节省
    EngineCount     map[string]int // 各引擎执行次数
}

const defaultMaxEntries = 10000 // 内存中最多保留 1 万条最近记录，防 OOM

// Meter 计量审计器（内存版生产级实现）
type Meter struct {
    mu         sync.RWMutex
    entries    []MeterEntry // 环形缓冲区
    head       int          // 写入指针
    count      int          // 当前条目数
    maxEntries int          // 最大容量

    // 增量统计字段 (O(1) 查询)
    totalSaved  int
    cacheHits   int
    engineSaved map[string]int
    engineCount map[string]int
}

// NewMeter 创建计量审计器
func NewMeter() *Meter {
    return &Meter{
        entries:     make([]MeterEntry, defaultMaxEntries),
        maxEntries:  defaultMaxEntries,
        engineSaved: make(map[string]int),
        engineCount: make(map[string]int),
    }
}

// Record 记录一次压缩计量 (O(1) 时间复杂度)
func (m *Meter) Record(e MeterEntry) {
    e.At = time.Now()

    m.mu.Lock()
    defer m.mu.Unlock()

    // 1. 环形缓冲区写入
    m.entries[m.head] = e
    m.head = (m.head + 1) % m.maxEntries
    if m.count < m.maxEntries {
        m.count++
    }

    // 2. 增量更新统计指标
    m.totalSaved += e.SavedTokens
    m.engineSaved[e.EngineName] += e.SavedTokens
    m.engineCount[e.EngineName]++

    if e.IsCached {
        m.cacheHits++
    }
}

// Stats 返回当前统计快照 (O(1) 时间复杂度)
func (m *Meter) Stats() MeterStats {
    m.mu.RLock()
    defer m.mu.RUnlock()

    // 拷贝 map，防止外部修改污染内部状态
    savedCopy := make(map[string]int, len(m.engineSaved))
    for k, v := range m.engineSaved {
        savedCopy[k] = v
    }
    countCopy := make(map[string]int, len(m.engineCount))
    for k, v := range m.engineCount {
        countCopy[k] = v
    }

    return MeterStats{
        TotalSaved:  m.totalSaved,
        TotalEntries: m.count,
        CacheHits:   m.cacheHits,
        EngineSaved: savedCopy,
        EngineCount: countCopy,
    }
}

// RecentEntries 返回最近 N 条记录 (O(N) 复制，不影响写锁)
func (m *Meter) RecentEntries(limit int) []MeterEntry {
    m.mu.RLock()
    defer m.mu.RUnlock()

    if limit <= 0 || limit > m.count {
        limit = m.count
    }

    out := make([]MeterEntry, limit)
    // 从环形缓冲区的最后一条记录开始往前取
    start := (m.head - 1 - limit + m.maxEntries) % m.maxEntries
    for i := 0; i < limit; i++ {
        idx := (start + i) % m.maxEntries
        out[i] = m.entries[idx]
    }
    return out
}

// Clear 清空记账本
func (m *Meter) Clear() {
    m.mu.Lock()
    defer m.mu.Unlock()

    m.head = 0
    m.count = 0
    m.totalSaved = 0
    m.cacheHits = 0
    m.engineSaved = make(map[string]int)
    m.engineCount = make(map[string]int)
}