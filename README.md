# Agent Platform Starter

一个适合 Go 后端工程师学习 AI Agent 的最小对话式平台。它先把最关键的基础打牢：用户身份、可持久化多会话、流式回答，以及可替换的大模型 Provider。后续可在此基础上增加工具调用、RAG、工作流、多智能体和观测能力。

后续迭代计划与验收标准见 [TODO.md](./TODO.md)。

学习文档：

- [上下文 Token 管理实现解析](./docs/context-token-management.md)
- [重新生成回答实现解析](./docs/regenerate-answer.md)

实现思路借鉴了优秀开源项目中经过验证的分层理念：前端会话体验参考 [LibreChat](https://github.com/danny-avila/LibreChat) / [Open WebUI](https://github.com/open-webui/open-webui) 的多 Provider 对话模式；Provider 使用类似 LangChain 的统一消息与流事件接口。这里没有复制它们的代码，而是以更小、更适合学习的 Go 版本重新实现。

## 已实现

- 邮箱密码注册、登录、JWT 鉴权
- 一个用户下创建、重命名、删除多个对话
- PostgreSQL 持久化用户、对话和完整消息历史
- 同时支持非流式 `Chat` 与 SSE `ChatStream`；流式推送 token 与模型思考增量，前端边接收边渲染
- OpenAI-compatible 适配器：OpenAI、Kimi、DeepSeek、GLM 复用同一协议实现
- Anthropic Messages API 原生 `Chat` / `ChatStream` 适配器
- 可选主动思考：通过 `ENABLE_THINKING=true` 让受支持的模型返回推理摘要
- OpenAI-compatible function calling：模型可请求服务端白名单工具，当前内置安全算术 `calculator`
- 通过环境变量启用模型；前端只看到已配置密钥的模型，密钥永不离开后端
- Zap JSON 结构化日志、全链路 `X-Request-ID`，以及每次模型调用的 token/成本记录
- Zap 日志同时输出到 stdout 与按日期轮转的本地 JSON 日志文件
- `LLMGateway` 统一模型调用：Token 预算预估、令牌桶限流、同步重试、幂等、流序号和观测
- 长对话上下文管理：模型级窗口、Token 裁剪、持久化滚动摘要和摘要失败降级
- 回答重新生成：复用原问题上下文、保留多个回答版本、版本切换、停止生成和幂等防重
- JSON Schema 结构化输出：统一请求、供应商适配、本地校验、`jsonb` 持久化与前端格式化展示
- Docker Compose 一键启动 PostgreSQL

## 快速启动

需要 Go 1.23+、Node.js 20+、Docker。

```bash
cp .env.example .env
# 编辑 .env：至少设置 JWT_SECRET，并填入一个模型的 API Key
docker compose up -d postgres redis

cd backend
go mod tidy
go run ./cmd/api
```

另开一个终端：

```bash
cd frontend
npm install
npm run dev
```

访问 http://localhost:5173 。后端健康检查为 http://localhost:8080/healthz。

## 目录与职责

```text
backend/
  cmd/api/                    # 组装依赖、启动 HTTP 服务
  internal/auth/              # 密码哈希、JWT
  internal/config/            # 环境变量和 Provider 目录
  internal/database/          # GORM 初始化、连接池、连通性检查
    db.go
    model/                     # GORM 表结构：用户、会话、消息
  internal/domain/            # 跨层领域错误与领域对象（按需扩展）
  internal/repository/        # Repository 接口、GORM 实现与迁移
  internal/service/           # 业务规则、模型调用与事务编排
  internal/httpapi/           # Gin 路由与 HTTP 横切能力
    server.go                 # HTTP 服务依赖组装
    router.go                 # Gin 路由与中间件注册
    handler/                  # HTTP 入参解析、调用 Service、返回响应
    middleware/               # 鉴权、CORS、日志、panic 恢复
    response/                 # 统一的 code/msg/data JSON 响应包装
  internal/logger/            # Zap 日志封装、Request ID 上下文
  internal/llmgateway/        # 统一模型调用入口：预算、限流、重试、幂等、观测
  internal/provider/          # 供应商协议适配器（仅由 LLMGateway 调用）
  internal/tool/              # 服务端工具定义、白名单与执行实现
frontend/src/                 # Vue 3 对话 UI
```

Repository 接口与 GORM 实现均位于 `repository`：例如 `UserRepository` 是接口，`GormUserRepository` 是实现。Service 依赖接口，因此单测仍可注入 fake 实现，而无需连接数据库。

## API 概览

所有 `/api/v1/*`（除 auth）均需 `Authorization: Bearer <token>`。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | 注册并返回 token |
| POST | `/api/v1/auth/login` | 登录 |
| GET | `/api/v1/models` | 已配置、可用的 Provider / 模型 |
| GET/POST | `/api/v1/conversations` | 对话列表 / 新建对话 |
| PATCH/DELETE | `/api/v1/conversations/:id` | 重命名 / 删除对话 |
| GET | `/api/v1/conversations/:id/messages` | 获取消息历史 |
| POST | `/api/v1/conversations/:id/messages` | SSE 流式发送消息 |
| POST | `/api/v1/conversations/:id/messages/chat` | 非流式发送消息，返回完整结果 |
| POST | `/api/v1/conversations/:id/messages/:messageId/regenerate` | SSE 重新生成指定 assistant 回答并创建新版本 |
| PATCH | `/api/v1/conversations/:id/messages/:messageId/activate` | 切换同一问题下的激活回答版本 |
| GET | `/api/v1/messages/:messageId/stream?afterSequence=N` | 按序号补发短期缓存中的 SSE 片段 |

流式接口依次发送 `metadata`、多个 `think`（模型思考增量）、多个 `delta`（最终回答增量）、最后的 `done` 事件。每个事件都携带 `messageId`、递增 `sequence`，并使用同值的 SSE `id` 字段。非正常终止发送 `interrupted` 或 `error`。

流式异常按四类处理：

- 用户取消：点击停止、删除正在生成的会话、退出或页面连接关闭都会触发 `AbortController` / `request.Context()`，逐层取消供应商请求、响应解析和工具调用。切换或新建会话不会取消原会话，原输出会在后台继续。除带严格 5 秒上限的消息终态写入外，不再启动后续任务；
- 超时：`STREAM_CONNECT_TIMEOUT` 分别约束 TCP 连接、TLS 握手和响应头；`STREAM_TTFT_TIMEOUT` 从供应商 HTTP 成功响应到首个协议事件；`STREAM_TOTAL_TIMEOUT` 约束整个模型及工具轮次。日志和 `finishReason` 分别记录 `connect_timeout`、`ttft_timeout`、`total_timeout`；
- 断流：OpenAI-compatible 必须收到 `[DONE]`，Anthropic 必须收到 `message_stop`。缺少结束标记时消息保存为 `interrupted`，保留半截内容但不会当作成功回答；正常结束会持久化供应商的 `finish_reason`；
- 重连：不会重新调用供应商并假装 Token 级续传。服务端按 `messageId` 缓存最近事件，前端断线后携带最后 `sequence` 请求补发。缓存默认保留 5 分钟；缓存缺口或过期时提示重新加载/生成。

供应商流一旦失效不会自动重放模型请求，因此不会因为所谓“重连”产生第二笔模型调用和重复 token 费用。客户端补发仅重放本服务已经缓存的 SSE 事件。

当前短期缓存是单进程内存实现，适合本地学习和单实例部署。多实例生产环境应替换为 Redis Stream 等共享存储，并让重连请求按 `messageId` 访问同一份事件序列。

## LLMGateway 调用边界

`ChatService` 不再直接调用供应商适配器。每一个模型轮次都经过 `LLMGateway`，其中 `LLMClient` 是供应商协议适配层，`TokenEstimator` 在请求发出前估算输入与输出预留 token，`RateLimiter` 先扣用户/供应商/模型维度的令牌桶，`Observation` 记录每次调用的结构化生命周期日志。

调用方应为一条用户消息提供 `Idempotency-Key` HTTP 请求头。该键会与用户、会话组成幂等记录，并覆盖从创建消息到完成/中断落库的整个业务消息；Gateway 内部的同步网络重试不会重复创建消息或重新获得幂等锁。前端已自动为每次发送生成该请求头。相同 key 仍在执行时返回 `409`，已完成时同样返回 `409`，客户端应加载会话中的既有结果。

`LLM_TOKEN_BUDGET`、`LLM_MAX_OUTPUT_TOKENS` 和 `LLM_RATE_TOKENS_PER_MINUTE` 控制请求准入；估算超预算或桶余额不足时，模型请求不会发到供应商。`LLM_SYNC_MAX_RETRIES` 只处理非流式、可重试的网络失败。流式调用绝不自动重放：Gateway 为每一个解析后的供应商事件增加 `modelSequence`，HTTP 层再为缓存/SSE 事件分配独立的 `sequence`；浏览器补发以 SSE `sequence` 去重，因此不会重复展示片段或重复计费。

`RateLimiter` 使用 Redis Lua 脚本实现原子令牌桶，支持多实例共享额度；`IdempotencyStore` 仍是进程内实现，生产多实例应将其替换为 Redis 等共享存储并设置合理 TTL。

## 上下文 Token 管理

每个 Provider 使用 `*_MODEL_LIMITS` 配置模型的部署上限，格式是逗号分隔的 `模型名:上下文窗口:最大输出Token`。例如：

```dotenv
OPENAI_MODELS=gpt-4o-mini
OPENAI_MODEL_LIMITS=gpt-4o-mini:32768:4096
```

实际预算取模型配置与全局 `LLM_TOKEN_BUDGET` / `LLM_MAX_OUTPUT_TOKENS` 中更严格的值。调用前会计算 system prompt、历史消息、当前问题、工具 JSON Schema、结构化输出约束及输出预留。每个模型通过 `*_MODEL_TOKENIZERS` 选择 `o200k_base`、`cl100k_base`、`p50k_base`、`r50k_base` 或 `heuristic`；tiktoken 兼容模型使用本地 BPE 词表，其他模型安全回退到保守估算。供应商返回的 usage 仍是最终计费和统计依据。若仅 system、工具和当前问题就已超限，请求返回 `422`，不会调用供应商。

历史超限时，服务优先保留 system prompt、当前问题和最近的完整对话轮次。默认复用当前会话模型压缩较早历史，也可以通过 `CONTEXT_SUMMARY_PROVIDER` 和 `CONTEXT_SUMMARY_MODEL` 路由到更便宜的小模型。摘要和质量评估均经过 `LLMGateway`，其 Token 和成本按各自模型价格累计，再与最终回答成本合并。

独立摘要 Provider 会接收被压缩的历史原文；生产环境必须根据数据驻留、隐私和供应商协议决定是否允许跨 Provider 路由。

候选摘要会经过结构化质量评估，只有 `passed=true` 且分数达到阈值才会成为活跃摘要；低质量或评估失败的候选只写入 `conversation_summary_versions` 审计表，本轮退化为保留最近历史。每个版本记录摘要/评估提示词版本、Provider、模型、消息断点、质量分数、问题列表、Token 和成本。活跃摘要及断点仍保存在 `conversations` 表，后续请求只读取断点之后的消息。

相关配置：

- `CONTEXT_SUMMARY_ENABLED`：是否允许生成滚动摘要，默认 `true`；
- `CONTEXT_SUMMARY_PROVIDER` / `CONTEXT_SUMMARY_MODEL`：独立摘要模型；同时留空时复用当前会话模型；
- `CONTEXT_SUMMARY_MAX_TOKENS`：单次摘要的最大输出预留，默认 `1024`；
- `CONTEXT_KEEP_RECENT_MESSAGES`：生成摘要时至少不参与摘要的最近消息数，默认 `8`；
- `CONTEXT_SUMMARY_MAX_CALLS`：一次用户请求最多进行的摘要调用次数，默认 `2`、最大 `5`；
- `CONTEXT_SUMMARY_QUALITY_ENABLED`：是否评估候选摘要，默认 `true`；
- `CONTEXT_SUMMARY_QUALITY_THRESHOLD`：摘要通过分数，默认 `70`；
- `CONTEXT_SUMMARY_QUALITY_MAX_TOKENS`：质量评估最大输出预留，默认 `256`。

日志事件 `conversation context prepared` 会记录 tokenizer、窗口、输出预留、原始/摘要/丢弃/最终 Token 估算、摘要模型、质量状态、摘要调用次数和最终保留的历史消息数。

非流式接口使用相同请求体（`content`、`provider`、`model`、可选 `temperature` 和 `outputFormat`），等待模型完整返回后返回 `userMessage` 与 `assistantMessage`。最终消息的 `content` 是回答，`think` 是模型返回的推理内容；不是所有模型或模型配置都会返回 `think`。无论使用哪一种接口，服务端都会执行相同的工具调用循环并记录 token/成本。

## JSON Schema 结构化输出

流式与非流式消息接口都可以携带服务端注册的 `outputFormat` ID。普通客户端不提交 JSON Schema：

```json
{
  "content": "提取联系人信息",
  "provider": "openai",
  "model": "gpt-4o-mini",
  "outputFormat": "contact_extraction"
}
```

`internal/outputformat` 是服务端输出格式注册表，当前内置 `contact_extraction` 和 `task_list`。新增业务格式时在注册表中增加 ID、说明和对应 Schema；浏览器只选择 ID，不能修改真正的数据契约。未注册的 ID 返回 `400`。

注册表解析得到的 `ResponseFormat` 只在后端内部流转。OpenAI 适配器会发送原生 `response_format.json_schema`；其他已接入 Provider 使用统一 Schema 提示约束。无论供应商是否原生支持，`LLMGateway` 都会在终态落库前解析并校验完整结果。非流式结果校验失败会按同步重试配置重新调用；流式结果已经向客户端发送过增量，因此不会自动重放，终态会返回错误。

校验成功后，原始 JSON 文本保留在 `assistantMessage.content`，解析后的对象保存在 `assistantMessage.structuredOutput` 和 PostgreSQL 的 `structured_output jsonb` 字段中。Schema 最大 64 KiB，名称只能使用 1 到 64 个字母、数字、下划线或连字符；出于安全考虑，仅允许本 Schema 内部的 `$ref`。工具调用中间轮次不做最终结果校验，工具执行完成后的最终回答才会校验。

除 `204 No Content` 与 SSE 接口外，REST 响应统一为：

```json
{"code": 0, "msg": "ok", "data": {}}
```

业务失败时 `code` 为 `-1`，并保留真实 HTTP 状态码（例如 `401`、`404`、`500`）；SSE 仍使用原始事件帧，不套 JSON 响应壳。

## 可观测性与成本

服务输出 Zap JSON 日志。每个请求都会生成或透传 `X-Request-ID`，并在响应头返回；业务日志和请求完成日志都会自动带上 `request_id`。调用侧使用 `logger.Info(ctx, ...)`、`logger.Error(ctx, ...)` 与字段助手（例如 `logger.String`、`logger.Err`），无需自行初始化或拼接日志字段。

每条日志会同时写入标准输出和 `LOG_DIR`。默认目录为 `./logs`，文件按本地日期命名为 `agent-platform-YYYY-MM-DD.log`；跨日首次写入会自动切换文件，`LOG_RETENTION_DAYS`（默认 `30`）控制过期文件清理。

每条 assistant 消息会额外保存：

- `think`：模型返回的思考/推理内容（若 Provider 支持）；
- `inputTokens`、`outputTokens`、`totalTokens`：模型 API 实际返回的 token 用量；
- `usageReported`：Provider 是否返回了可用用量；
- `costNanoUsd`、`costKnown`：根据部署配置计算的成本及其有效性。

OpenAI-compatible Provider 会请求 `stream_options.include_usage`，以取得流式回答最后一个用量块；若某个兼容服务不支持该字段，可以设置对应的 `*_STREAM_INCLUDE_USAGE=false`。Anthropic 的原生流事件会直接解析其用量字段。

成本单价由部署者配置，避免将会变动的厂商价格硬编码进项目。以 OpenAI 为例：

```dotenv
# 单位：美元 / 百万 input token : 美元 / 百万 output token
OPENAI_MODEL_PRICING=gpt-4o-mini:0.15:0.60
```

可使用逗号配置多个模型。成本以纳美元整数保存（`1 USD = 1,000,000,000 nano USD`），避免浮点精度问题；未配置模型单价时，token 用量仍会保存，但 `costKnown=false`。

## 主动开启 Think

默认不要求模型输出推理。部署时可设置：

```dotenv
ENABLE_THINKING=true
# Anthropic 内部推理的 token 上限；必须在 1024 到 4095 之间，默认 2048。
THINKING_BUDGET_TOKENS=2048
```

开关启用后，服务端仅向已确认支持的组合发送原生参数：DeepSeek V4、GLM `4.5+` / `5+` 与 Claude Sonnet / Opus。OpenAI、Kimi 以及不匹配的模型仍按普通对话请求，因此不会因为非标准参数而失败。DeepSeek 与 GLM 发送 `thinking: {"type":"enabled"}`；Anthropic 发送原生的 `thinking` 配置和预算，并要求返回可展示的推理摘要。

主动思考通常增加首 token 延迟和输出 token 成本。对于 DeepSeek 与 Anthropic，开启 Think 时服务端不会同时发送 `temperature`，以符合其 API 的兼容性要求。模型仍可能自行决定不返回思考内容；此时 `think` 为空，前端不会显示思考区。

## 服务端 Tool Calling

OpenAI-compatible Provider（OpenAI、Kimi、DeepSeek、GLM）在每次请求中携带已注册工具的 JSON Schema，并使用 `tool_choice: auto`。当模型流式返回 `tool_calls` 时，后端会完整拼接分片参数、校验工具白名单、在服务端执行工具，再将结果作为 `role: "tool"` 消息回传给模型，直到得到最终回答。

当前默认注册 `calculator`，只支持数字、`+`、`-`、`*`、`/` 与括号，不会执行任意代码、Shell 命令或网络请求。新增工具只需在 `internal/tool` 实现 `Tool` 接口，并加入 `tool.NewDefaultRegistry`。

`MAX_TOOL_ITERATIONS` 默认是 `4`，最多允许同一用户轮次内执行四轮工具调用；上限为 `10`，防止模型异常循环。工具调用的中间协议消息仅在当前模型轮次内保留，最终用户消息与 AI 最终回答仍会持久化到 PostgreSQL；后续可在审计/Trace 功能中扩展为完整工具调用记录。

## 设计要点与下一步

`provider.Provider` 是核心扩展点。新增供应商只需实现 `Chat(ctx, request) (ChatResponse, error)` 与 `ChatStream(ctx, request) <-chan StreamEvent`，然后在 `provider.NewRegistry` 注册；上层 HTTP 和数据库完全不用改。OpenAI 兼容协议的供应商只需新增一段配置。

建议按这个顺序扩展：

1. 增加文档上传、切分、embedding、pgvector 检索（RAG）。
2. 为 Agent 增加循环上限、工具权限、审计和可观测 trace。

生产环境请使用强随机 `JWT_SECRET`、HTTPS、受限 CORS、密钥管理服务与数据库备份；目前的自动迁移适合作为学习与开发起点。
