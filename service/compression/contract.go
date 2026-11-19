package compression

import (
    "context"
    "errors"
    "time"
)

// ErrNilInput 输入为 nil 时的错误（fail-closed 用）
var ErrNilInput = errors.New("compression: nil input")

// ============ B 组：分区与角色 ============
type Region int

const (
    RegionFrozen Region = iota
    RegionArchived
    RegionHot
)

// String 增加边界检查，防止未知值导致 panic
func (r Region) String() string {
    switch r {
    case RegionFrozen:
        return "frozen"
    case RegionArchived:
        return "archived"
    case RegionHot:
        return "hot"
    default:
        return "unknown"
    }
}

type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

// ============ H 组：安全分级 ============
type Stage int

const (
    S0 Stage = iota // 完全无损
    S1              // 结构化无损
    S2              // 低风险有损
    S3              // 中风险有损
    S4              // 高风险有损
)

// String 方便日志打印和审计
func (s Stage) String() string {
    switch s {
    case S0:
        return "S0_lossless"
    case S1:
        return "S1_structured_lossless"
    case S2:
        return "S2_low_risk_lossy"
    case S3:
        return "S3_medium_risk_lossy"
    case S4:
        return "S4_high_risk_lossy"
    default:
        return "unknown_stage"
    }
}

// ============ H 组：兜底模式 ============
type Mode int

const (
    FailOpen Mode = iota
    FailClosed
)

func (m Mode) String() string {
    switch m {
    case FailOpen:
        return "fail_open"
    case FailClosed:
        return "fail_closed"
    default:
        return "unknown_mode"
    }
}

// ============ F 组：管道堆叠兼容性 ============
type OutputKind int

const (
    KindProse OutputKind = iota
    KindStructured
    KindHybrid
)

func (k OutputKind) String() string {
    switch k {
    case KindProse:
        return "prose"
    case KindStructured:
        return "structured"
    case KindHybrid:
        return "hybrid"
    default:
        return "unknown_kind"
    }
}

// ============ H 组：只读区段与透明标记 ============
type Span struct {
    Start int
    End   int
    Kind  string // "code"|"url"|"path"|"command"|"number"|"json"
}

type Elision struct {
    Kind   string // "lines"|"chars"|"matches"|"keys"|"turns"
    Count  int
    Reason string // 人类可读原因，进审计日志
}

// ============ J 组：预留指标接口 ============
type Metrics struct {
    EngineName       string
    OriginalTokens   int
    CompressedTokens int
    SavedTokens      int
    IsCached         bool
}

// ============ 输入 / 输出 ============
type Input struct {
    Region        Region
    Role          Role
    Content       any
    TaskType      string
    AllowLossy    bool
    Query         string // 最后一条用户查询（供 Relevance 等历史区压缩器做相关性评分）
    Command       string // shell 命令（供 RTK 命令感知压缩，从 assistant tool_calls 解析）
    ReadOnlySpans []Span
    CallCtx       *CallContext
}

type Output struct {
    Content       any
    SavedTokens   int
    Elided        []Elision
    Hash          string
    OutputKind    OutputKind
    Deterministic bool
    Metrics       Metrics
    // 遥测（命令感知等引擎返回，供审计结构化展示）
    Techniques []string // 使用的技术，如 "rtk-filter"、"command-aware"
    Rules      []string // 应用的规则名，如 "build-vite:keep"
    Confidence float64  // 命令类型置信度 0~1
}

type CallContext struct {
    RequestID string
    SessionID string
    Model     string
    Now       time.Time
}

// ============ 统一压缩器契约 ============
type Compressor interface {
    Name() string
    Stage() Stage
    Mode() Mode
    OutputKind() OutputKind
    IsApplicable(in *Input) bool
    Apply(ctx context.Context, in *Input) (*Output, error)
}

// FailOpenOutput 提供给各压缩器的 fail-open 兜底函数
// 增加 nil 防御，确保网关绝对不因压缩器 panic 而崩溃
func FailOpenOutput(in *Input) *Output {
    if in == nil {
        // 返回一个绝对安全的空输出
        return &Output{
            Content:       "",
            OutputKind:    KindProse,
            Deterministic: true,
            Metrics: Metrics{
                EngineName: "fail-open-nil-defense",
            },
        }
    }

    return &Output{
        Content:       in.Content,
        SavedTokens:   0,
        Elided:        nil,
        Hash:          "",
        OutputKind:    KindProse,
        Deterministic: true,
        Metrics: Metrics{
            EngineName: "fail-open",
        },
    }
}