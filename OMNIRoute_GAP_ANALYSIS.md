# token-comper-demo vs OmniRoute —— 差距分析与路线图

> 生成日期：2026-08-20
> 依据：本地 OmniRoute 源码（`node_modules/omniroute/src` + `open-sse`）与本项目 `token-comper-demo` 逐文件核对
> 结论：**算法命名矩阵已基本对齐，真实差距在「压缩质量/智能层」与「系统外壳层」两块**

---

## 0. 一句话结论

你的 demo 在「压缩引擎的**种类**」上已经和 OmniRoute 对得上（caveman/rtk/gcf/ccr/relevance/llmlingua/session/tool-prune 基本 1:1，且 `omniglyph` 是你多做、对方没有的）。你觉得「差很多」是对的，但差在两层：

1. **质量层**：OmniRoute 有真正调用模型做压缩/摘要、自适应调优、多语言规则；你的压缩几乎全是启发式规则。
2. **系统层**：OmniRoute 是透明代理网关（自动拦截流量）+ 流式 + 持久化 + 可观测 + 多模型路由；你的 demo 是一个显式 `/compress` HTTP 接口。

---

## 1. 已对齐、差距很小的部分 ✅

| 维度 | 说明 |
|---|---|
| 引擎命名矩阵 | caveman / rtk / gcf / ccr / relevance / llmlingua / session / tool-prune 在双方都存在且意图一致 |
| 保真度闸门 (fidelity gate) | 双方都有：OmniRoute `open-sse/.../fidelityGate.ts`；本项目 `service/compression/fidelity.go` |
| 有损/无损分级 | 双方都区分 lossy / lossless，并用 AdminPolicy 控制 |
| `omniglyph`（长文转图） | **本项目独有**，OmniRoute 源码中未见对应实现 |

---

## 2. 差距一：压缩质量 / 智能层（最核心差距）

> OmniRoute 源码位置：`open-sse/services/compression/**` 与 `lib/compression/**`

| # | 能力 | OmniRoute 实现 | 本项目现状 | 补齐需要做什么 |
|---|---|---|---|---|
| Q1 | **真·LLM 压缩引擎** | `engines/llm`（调用模型对内容做语义重写/压缩） | ❌ 无。`llmlingua` 仅是启发式删停用词 | 新增一个调用上游模型的 compressor（带护栏/预算），这是质量上最本质的差距 |
| Q2 | **模型摘要 / 渐进老化 / 读取生命周期** | `summarizer.ts`、`progressiveAging.ts`、`engines/readLifecycle` | 只有占位+规则的 session / ccr | 用模型对长历史做摘要归档，而非简单占位替换 |
| Q3 | **多语言规则集** | `rules/` 含 `ja / zh / pt-BR / de / id / fr / es / en` 共 8 套 | 仅中文启发式（`zh_filler` 等） | 抽象语言规则接口，至少补齐 en + ja |
| Q4 | **自适应调优** | `adaptiveCompression/`（ladder、computeTarget、resolveAdaptivePlan）+ `strategySelector.ts` + `deriveDefaultPlan.ts` | 固定 `Intensity` + 手动 policy | 根据预算/余量自动选策略、自动调强度档位（lite/aggressive/ultra） |
| Q5 | **硬预算 / 风险闸门** | `hardBudget.ts`、`riskGate/`（riskPatterns、strategyWrap） | 仅有基础 guard | 加入硬 token 上限强制执行 + 压缩前风险模式检测 |
| Q6 | **裁判模型校验** | `lib/compression/judgeModelClient.ts`（保真度校验用 LLM 裁判） | 规则式 fidelity 校验 | 可选：用 LLM 裁判替代/增强当前规则校验 |

**小结**：Q1（真 LLM 引擎）是拉开质量差距的关键；Q4（自适应）是体验差距的关键。

---

## 3. 差距二：系统外壳层（让你觉得它是「产品」的部分）

> OmniRoute 源码位置：`src/mitm/**`、`src/sse/**`、`src/server/**`、`lib/db/**`、`lib/usage/**`

| # | 能力 | OmniRoute 实现 | 本项目现状 | 补齐需要做什么 |
|---|---|---|---|---|
| S1 | **透明 MITM 代理** | `mitm/`（manager、tproxy、cert、dns、sudoGate、upstreamTrust）夹在客户端↔上游之间自动拦截 | ❌ 无。需调用方自己拼 `/compress` | 做一层 HTTP 反向代理，拦截 chat 请求→压缩→转发；客户端无感知 |
| S2 | **流式 + 实时面板** | `sse/`（handlers/chat）、`server/ws`（liveServer） | 无流式；web 仅 3 个静态 tab | 支持 SSE 流式压缩转发 + WebSocket 实时看板 |
| S3 | **持久化 + 可观测** | `storage.sqlite`（+wal/shm）、`compressionAnalyticsWrite`、`compressionUsageReceipt`、`compressionCacheStats` | 仅内存 `stats`（进程重启即丢） | 引入 SQLite/Redis 存运行记录、用量回执、缓存命中统计 |
| S4 | **多模型路由（"Omni" 本体）** | `lib/usage/routeExplain`、`comboForecast`、跨厂商 failover | 单上游（ark volces） | 增加 provider 抽象 + 路由/容灾/预算调度 |
| S5 | **安全 + MCP 暴露** | `server/auth`、`cors`、`authz`；`mcp-server/tools/compressionTools.ts` | `/admin` 无鉴权 | 加 admin 鉴权；把压缩能力以 MCP tool 形式暴露 |

**小结**：S1（透明代理）是「从 API 变成网关」的关键；S3（持久化/可观测）是「能长期跑、能复盘」的关键。

---

## 4. 路线图（分阶段，供决策）

### Phase 0 —— 差距清单（本文件）
- 目标：对齐认知。✅ 完成

### Phase 1 —— 压缩质量/智能层（**推荐优先**，最贴合 demo 定位）
1. Q1 真 LLM 压缩引擎（带护栏 + 预算）
2. Q4 自适应档位（lite/aggressive/ultra + 自动选策略）
3. Q2 模型摘要替代占位 CCR
4. Q3 多语言规则（en + ja）
- 产出：压缩质量从「规则近似」升级到「模型驱动」，且可自适应。

### Phase 2 —— 系统外壳（让 demo 像产品）
1. S3 SQLite 持久化 + telemetry 面板
2. S1 透明代理（反向代理拦截 chat 流量）
3. S2 SSE 流式 + WebSocket 实时看板
- 产出：从「手动调的接口」变成「自动生效的网关」。

### Phase 3 —— 路由与开放（可选）
1. S4 多模型路由 / failover
2. S5 admin 鉴权 + MCP tool 暴露
- 产出：接近 OmniRoute 的完整形态。

---

## 5. 建议与取舍

- 若目标是**演示 token 压缩技术本身**：优先 Phase 1（Q1 真 LLM 引擎 + Q4 自适应），这才是和 OmniRoute 在「压缩能力」上能正面比较的点。
- 若目标是**做一个能长期给别人用的网关**：优先 Phase 2 的 S1+S3，否则每次重启丢数据、还要手动拼接口。
- OmniRoute 的「Omni」路由（S4）和它的压缩是两条线，压缩 demo 不一定要追这一块。
- 你已有的 `omniglyph` 是对方没有的亮点，可作为 demo 差异化卖点保留并强化。

> 下一步：从上述 Phase 中选定一块，我再给出具体的实现计划与代码改动。
