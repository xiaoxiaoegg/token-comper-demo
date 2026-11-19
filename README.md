# Token 压缩网关 Demo · 使用说明

一个 Go 写的 token 压缩网关 demo：把对话按「区域 + 角色 + 冗余类型」分而治之，用可插拔引擎各自压缩后，转发给上游大模型，并实时展示每个引擎省了多少 token。

---

## 一、快速启动

```bash
cd token-comper-demo

# 1. 首次使用：配置上游模型（火山方舟等 OpenAI 兼容接口）
cat > .env <<'EOF'
UPSTREAM_BASE_URL=https://ark.cn-beijing.volces.com/api/coding/v3
UPSTREAM_API_KEY=你的key
UPSTREAM_MODEL=doubao-seed-code
EOF

# 2. 启动（二选一）
./start.sh              # 推荐：后台运行，打印内网 IP
# 或
go build -o token-compressor . && ./token-compressor

# 3. 打开浏览器
# 本机  http://localhost:4040
# 内网  http://<本机IP>:4040   （start.sh 会打印）
```

> 端口默认 `4040`，可用环境变量 `PORT` 覆盖（如 `PORT=8080 ./token-compressor`）。

---

## 二、界面五大面板

| 面板 | 干什么 |
|---|---|
| 👤 **用户** | 粘贴 OpenAI 格式请求体 → 点「压缩并调用模型」→ 看**原始 vs 压缩后** diff、总节省、上游真实回答 |
| 🧪 **单组件测试** | 单独挑一个引擎喂数据看效果（带示例和说明） |
| 🎬 **Studio** | 最近一次压缩的**引擎级联**（每个引擎生效/跳过 + 节省 token），每 2 秒刷新 |
| 🛡️ **管理员** | 配默认策略、强度上限、管理 API key |
| 🚫 **排除规则** | 针对 model / endpoint 配置「跳过压缩」或「仅无损」 |

---

## 三、核心 API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/compress` | 完整压缩流水线 + 真实调用上游 |
| POST | `/test/engine` | 单引擎测试（`engine`/`content`/`query`/`command`） |
| GET | `/stats` | 累计计量统计 |
| GET | `/compression/studio` | 最近级联 + 累计统计 |
| GET/POST | `/admin/policy` | 读/改默认策略 |
| POST/GET/PUT/DELETE | `/admin/keys` | API key 管理 |
| GET/POST/DELETE | `/admin/exclusions` | 排除规则 |
| GET | `/health` | 健康检查 |

调用示例（绑定策略的 key）：

```bash
curl -X POST http://localhost:4040/compress \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-xxxx' \
  -d '{"model":"doubao-seed-code","messages":[{"role":"user","content":"你好"}]}'
```

---

## 四、压缩引擎

**14 个原子引擎**（按角色自动分派）：

| 引擎 | 作用 | 有损 |
|---|---|---|
| normalize | 清多余空格/空行/重复标点 | ❌ |
| caveman | 精简系统提示词客套话 | ❌ |
| tool-prune | 按角色裁剪工具定义 | ❌ |
| prefix | 前缀对齐三区（冻结/封存/热区） | ❌ |
| gcf | 同质 JSON 数组转列式 | ❌ |
| session | 会话去重 | ❌ |
| multimodal | 图片 detail 降级 | ❌ |
| omniglyph | 长文本转图片 | ❌ |
| rtk | 工具结果按命令/类型压缩 | ⚠️ |
| ccr | 超长历史归档占位 | ⚠️ |
| relevance | 按查询过滤历史不相关句 | ⚠️ |
| llmlingua | 删低信息词（语义压缩） | ⚠️ |
| summarize | 抽取式摘要（保首末句+相关句） | ⚠️ |
| age | 渐进老化（丢中段陈旧内容） | ⚠️ |

**3 个复合预设**（一键组合）：

- `lite` = normalize + caveman + tool-prune + gcf（纯无损）
- `aggressive` = lite + 全部有损（含摘要/老化/相关性）
- `ultra` = aggressive + omniglyph（最强）

---

## 五、管理员操作要点

- **默认策略**：`default_plugins` 控制用户不指定时开哪些引擎；`max_lossy` 控制是否允许有损；`intensity_ceil` 控制强度上限。
- **有损双层门**：有损引擎需要「管理员 `max_lossy` = true」**且**「该 API key 的 `lossy` = true」才生效。
- **API key**：每个 key 绑定一套压缩策略（插件 + 强度 + 有损开关），用户带 `Authorization: Bearer <key>` 调用即生效。
- **排除规则**：`type=model|endpoint`，`pattern` 子串匹配，`action=skip|lossless`。适合对特定模型/端点强制不压缩。

---

## 六、常见问题

| 现象 | 原因 / 解决 |
|---|---|
| 启动报 `address already in use` | 旧实例占着 4040 端口 → `pkill -f token-compressor` 后重启 |
| 内网 `curl` 访问 502 | 本机代理干扰 → 加 `--noproxy '*'` |
| WorkBuddy 里起的服务一会就没了 | 会话结束会回收后台进程 → 在自己终端跑 `./start.sh` |
| 上游返回 400/404 | 检查 `.env` 里 model 是否支持 coding plan，或 key 是否有效 |
