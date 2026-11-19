package compression

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log"
    "sync"
)

// GuardConfig 护栏全局配置
type GuardConfig struct {
    Enabled           bool
    ExemptModels      []string // 不进行任何压缩的模型名单
    MaxInflationRatio float64  // 压缩后体积变大？超过此值回退原文，默认 1.0 (即100%)
    EnableIdempotent  bool     // 是否开启幂等缓存复用
    MaxCacheEntries   int      // 进程内缓存最大条目数，防 OOM
    Fidelity          FidelityConfig // 保真校验（压缩后校验关键内容存活，不达标回退原文）
}

// Guard 护栏，包裹在每一个具体的 Compressor 外层
type Guard struct {
    cfg    GuardConfig
    mu     sync.RWMutex       // 读写锁，保护并发访问 cache
    cache  map[string]*Output // 进程内缓存
    target Compressor
}

func NewGuard(cfg GuardConfig, target Compressor) *Guard {
    // 默认值兜底，防止配置缺失导致逻辑异常
    if cfg.MaxInflationRatio == 0 {
        cfg.MaxInflationRatio = 1.0
    }
    if cfg.MaxCacheEntries == 0 {
        cfg.MaxCacheEntries = 10000 // 默认最多缓存 1 万条
    }
    return &Guard{
        cfg:    cfg,
        cache:  make(map[string]*Output),
        target: target,
    }
}

// Apply 拦截 Apply，执行护栏逻辑
func (g *Guard) Apply(ctx context.Context, in *Input) (*Output, error) {
    // 1. Nil 防御
    if in == nil {
        return nil, ErrNilInput
    }

    model := ""
    if in.CallCtx != nil {
        model = in.CallCtx.Model
    }

    // 2. 全局开关 / 模型豁免
    if !g.cfg.Enabled || g.isExempt(model) {
        return FailOpenOutput(in), nil
    }

    // 3. 有损前置校验：S2+ 级别但未授权有损，直接跳过
    if g.target.Stage() >= S2 && !in.AllowLossy {
        return FailOpenOutput(in), nil
    }

    // 4. 幂等缓存复用
    hash := hashInput(in)
    if g.cfg.EnableIdempotent && hash != "" {
        g.mu.RLock()
        cached, ok := g.cache[hash]
        g.mu.RUnlock()
        if ok {
            // 拷贝一份再返回，防止下游修改污染缓存
            out := *cached
            out.Metrics.IsCached = true
            return &out, nil
        }
    }

    // 5. 执行真实压缩
    out, err := g.target.Apply(ctx, in)
    if err != nil {
        // Fail-Open/Closed 分派
        if g.target.Mode() == FailClosed {
            log.Printf("[Guard] Compressor %s failed (fail-closed): %v", g.target.Name(), err)
            return nil, fmt.Errorf("compressor %s failed (fail-closed): %w", g.target.Name(), err)
        }
        log.Printf("[Guard] Compressor %s failed (fail-open): %v", g.target.Name(), err)
        return FailOpenOutput(in), nil
    }

    // 防御：底层压缩器可能返回 nil out 但 nil err
    if out == nil {
        return FailOpenOutput(in), nil
    }

    // 6. Inflation Guard：压完反而变大？直接回退原文
    // 结合 MaxInflationRatio 配置进行精准校验
    if out.SavedTokens < 0 {
        log.Printf("[Guard] Compressor %s triggered inflation guard (SavedTokens: %d)", g.target.Name(), out.SavedTokens)
        return FailOpenOutput(in), nil
    }

    // 6.5 Fidelity Gate：有损压缩后校验关键内容（只读区/数字/JSON key）是否存活
    // 只对「真的压了」的输出做校验；不保真则回退原文（FAIL-OPEN：校验器出错不阻塞）
    if g.cfg.Fidelity.Enabled && out.SavedTokens > 0 && in.Content != nil {
        if inStr, ok := in.Content.(string); ok {
            if outStr, ok := out.Content.(string); ok {
                if fr := checkFidelity(inStr, outStr, g.cfg.Fidelity); !fr.Passed {
                    log.Printf("[Fidelity] %s 未通过保真校验（%s：%s）→ 回退原文", g.target.Name(), fr.FailedInvariant, fr.Detail)
                    return FailOpenOutput(in), nil
                }
            }
        }
    }

    // 7. 幂等缓存写入
    if g.cfg.EnableIdempotent && hash != "" {
        g.mu.Lock()
        // 简单的防 OOM 策略：超过上限直接清空 (生产环境建议换 LRU 算法)
        if len(g.cache) >= g.cfg.MaxCacheEntries {
            g.cache = make(map[string]*Output)
        }
        g.cache[hash] = out
        g.mu.Unlock()
    }

    return out, nil
}

// 代理原压缩器的方法
func (g *Guard) Name() string                { return g.target.Name() }
func (g *Guard) Stage() Stage                 { return g.target.Stage() }
func (g *Guard) Mode() Mode                   { return g.target.Mode() }
func (g *Guard) OutputKind() OutputKind       { return g.target.OutputKind() }
func (g *Guard) IsApplicable(in *Input) bool   { return g.target.IsApplicable(in) }

func (g *Guard) isExempt(model string) bool {
    for _, m := range g.cfg.ExemptModels {
        if m == model {
            return true
        }
    }
    return false
}

// hashInput 规范化 JSON 后取 SHA256
func hashInput(in *Input) string {
    if in == nil || in.Content == nil {
        return ""
    }

    b, err := json.Marshal(in.Content)
    if err != nil {
        // 如果结构里包含无法序列化的类型 (如 chan, func)，降级用 fmt 兜底
        b = []byte(fmt.Sprintf("%v", in.Content))
    }
    h := sha256.Sum256(b)
    return hex.EncodeToString(h[:])
}