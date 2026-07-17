# Agent Platform Starter

一个适合 Go 后端工程师学习 AI Agent 的最小对话式平台。它先把最关键的基础打牢：用户身份、可持久化多会话、流式回答，以及可替换的大模型 Provider。后续可在此基础上增加工具调用、RAG、工作流、多智能体和观测能力。

实现思路借鉴了优秀开源项目中经过验证的分层理念：前端会话体验参考 [LibreChat](https://github.com/danny-avila/LibreChat) / [Open WebUI](https://github.com/open-webui/open-webui) 的多 Provider 对话模式；Provider 使用类似 LangChain 的统一消息与流事件接口。这里没有复制它们的代码，而是以更小、更适合学习的 Go 版本重新实现。

## 已实现

- 邮箱密码注册、登录、JWT 鉴权
- 一个用户下创建、重命名、删除多个对话
- PostgreSQL 持久化用户、对话和完整消息历史
- SSE 推送 token 增量，前端边接收边渲染
- OpenAI-compatible 适配器：OpenAI、Kimi、DeepSeek、GLM 复用同一协议实现
- Anthropic Messages API 原生流式适配器
- 通过环境变量启用模型；前端只看到已配置密钥的模型，密钥永不离开后端
- Zap JSON 结构化日志、全链路 `X-Request-ID`，以及每次模型调用的 token/成本记录
- Docker Compose 一键启动 PostgreSQL

## 快速启动

需要 Go 1.23+、Node.js 20+、Docker。

```bash
cp .env.example .env
# 编辑 .env：至少设置 JWT_SECRET，并填入一个模型的 API Key
docker compose up -d postgres

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
  internal/provider/          # 统一模型接口与协议适配器
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
| GET/PATCH/DELETE | `/api/v1/conversations/:id` | 消息历史 / 重命名 / 删除 |
| POST | `/api/v1/conversations/:id/messages` | SSE 流式发送消息 |

流式接口依次发送 `metadata`、多个 `delta`、最后的 `done` 事件；发生错误时发送 `error`。客户端中断连接会保留已生成的内容，并将消息状态标记为 `stopped`。

除 `204 No Content` 与 SSE 接口外，REST 响应统一为：

```json
{"code": 0, "msg": "ok", "data": {}}
```

业务失败时 `code` 为 `-1`，并保留真实 HTTP 状态码（例如 `401`、`404`、`500`）；SSE 仍使用原始事件帧，不套 JSON 响应壳。

## 可观测性与成本

服务输出 Zap JSON 日志。每个请求都会生成或透传 `X-Request-ID`，并在响应头返回；业务日志和请求完成日志都会自动带上 `request_id`。调用侧使用 `logger.Info(ctx, ...)`、`logger.Error(ctx, ...)` 与字段助手（例如 `logger.String`、`logger.Err`），无需自行初始化或拼接日志字段。

每条 assistant 消息会额外保存：

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

## 设计要点与下一步

`provider.Provider` 是核心扩展点。新增供应商只需实现 `ChatStream(ctx, request) <-chan StreamEvent`，然后在 `provider.NewRegistry` 注册；上层 HTTP 和数据库完全不用改。OpenAI 兼容协议的供应商只需新增一段配置。

建议按这个顺序扩展：

1. 增加 OpenAI tool calling，工具在服务端注册与执行。
2. 增加文档上传、切分、embedding、pgvector 检索（RAG）。
3. 为 Agent 增加循环上限、工具权限、审计和可观测 trace。

生产环境请使用强随机 `JWT_SECRET`、HTTPS、受限 CORS、密钥管理服务与数据库备份；目前的自动迁移适合作为学习与开发起点。
