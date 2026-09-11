# RAG 第二阶段：EmbeddingGateway 与模型适配

本文对应 `TODO.md` 的“10. EmbeddingGateway 与模型适配”。第一阶段解决“向量存在哪里”，第二阶段解决“业务怎样安全、稳定地得到向量”。

完成本阶段后，系统具备：

- 独立于聊天模型的 `EmbeddingClient` 与 `EmbeddingGateway`；
- OpenAI-compatible `POST /embeddings` 协议适配；
- 请求前 Token 估算和单输入限制；
- 同时按输入数量和 Token 数量拆分供应商批次；
- 超时、有限重试和 `Retry-After` 处理；
- 返回数量、索引、维度及浮点值校验；
- 按用户隔离的 Redis 内容哈希缓存；
- Token、成本、缓存命中、请求次数和重试次数观测。

这一阶段仍然没有知识库上传接口。下一阶段的摄取 Service 会把文档切成 Chunk，再调用本阶段的 Gateway，将向量写入第一阶段创建的 `knowledge_chunks.embedding`。

## 1. 为什么不直接使用 LLMGateway

Chat 和 Embedding 都会调用模型供应商，但两类请求的业务语义不同：

| 对比项 | Chat | Embedding |
| --- | --- | --- |
| 输入 | 角色消息、工具、图片、Schema | 一批纯文本 |
| 输出 | 文本或流式事件 | 固定维度浮点数组 |
| 关键约束 | 上下文窗口、最大输出 Token | 单输入 Token、批量总 Token、向量维度 |
| 流式 | 需要 | 不需要 |
| 重试风险 | 可能重复输出或工具副作用 | 同输入幂等，适合有限重试 |
| 缓存键 | 对话状态复杂，不适合简单缓存 | 文本内容哈希非常适合缓存 |

若把 Embedding 塞进 `LLMGateway`，接口会充满只对某一类模型有意义的参数和分支。独立 Gateway 让聊天业务与文档摄取都只依赖各自需要的最小接口。

```text
DocumentIngestionService
        │
        ▼
EmbeddingGateway  ───────► Redis vector cache
        │ cache miss
        ▼
EmbeddingClient
        │
        ▼
OpenAI-compatible POST /embeddings
```

对应代码：

- `internal/embeddinggateway/gateway.go`：业务入口与编排；
- `internal/embeddinggateway/token_estimator.go`：请求前 Token 估算；
- `internal/embeddinggateway/cache.go`：Redis 向量缓存；
- `internal/embeddinggateway/factory.go`：按配置组装正式实现；
- `internal/provider/embedding.go`：OpenAI-compatible HTTP 协议适配。

## 2. 接口边界

供应商适配器实现最小 `EmbeddingClient`：

```go
type EmbeddingClient interface {
    Embed(ctx context.Context, request provider.EmbeddingRequest) (
        provider.EmbeddingResponse,
        error,
    )
}
```

业务层只允许依赖 Gateway：

```go
type Gateway interface {
    EmbedDocuments(ctx context.Context, request Request) (Result, error)
    EmbedQuery(ctx context.Context, request Request) (Result, error)
}
```

文档与查询分成两个方法，是为了给后续扩展保留明确语义。例如某些模型要求文档和查询使用不同 task type 或前缀。当前 OpenAI-compatible 实现发送相同协议，但上层不需要因此失去边界。

`EmbedQuery` 强制只有一条文本，`EmbedDocuments` 可以接收多条。两者最终走相同的校验、缓存和供应商调用逻辑。

## 3. OpenAI-compatible 协议

适配器发送：

```http
POST /v1/embeddings
Authorization: Bearer <API_KEY>
Content-Type: application/json
```

```json
{
  "model": "text-embedding-3-small",
  "input": ["第一段文本", "第二段文本"],
  "dimensions": 1536,
  "encoding_format": "float"
}
```

响应中的 `data` 不应依赖数组当前顺序，而应按照 `index` 对齐原输入：

```json
{
  "data": [
    {"index": 1, "embedding": [0.1, 0.2]},
    {"index": 0, "embedding": [0.3, 0.4]}
  ],
  "usage": {"prompt_tokens": 12, "total_tokens": 12}
}
```

Gateway 会检查 index 是否越界、重复或缺失，再恢复输入顺序。如果直接假设响应数组顺序一致，一旦兼容服务调整排序，向量就可能被写到错误的 Chunk，这是非常隐蔽的数据污染。

`dimensions` 仅由部分模型支持。OpenAI `text-embedding-3` 系列支持缩短维度；其他兼容服务若拒绝该字段，可设置：

```dotenv
EMBEDDING_SEND_DIMENSIONS=false
```

即使不发送该字段，`EMBEDDING_DIMENSION` 仍用于校验实际返回向量，防止不同维度混入同一索引。

官方 API 当前规定每个输入最多 8192 Token、一个请求内所有输入合计最多 300,000 Token，并允许一个请求传入字符串数组。项目默认使用更保守的单输入 8191、单批合计 100,000，便于兼容限制更严格的服务。参见 [OpenAI Create embeddings API](https://developers.openai.com/api/reference/resources/embeddings/methods/create)。

## 4. 请求前 Token 估算

`EmbeddingGateway` 不会先把超长文本发给供应商再等待 400。它先对所有输入计数，任何单条文本超过 `EMBEDDING_MAX_INPUT_TOKENS`，整次业务调用会在供应商请求发生前失败。

当前支持：

- `cl100k_base`：默认用于 OpenAI Embedding 模型；
- `o200k_base`、`p50k_base`、`r50k_base`：兼容部署可选择；
- `heuristic`：没有兼容 tokenizer 时的保守回退。

最终用量始终以供应商返回的 `usage.prompt_tokens` 为准。Tokenizer 估算用于请求准入和批次规划，不冒充账单数据。

## 5. 双限制批处理

供应商通常同时限制“一个请求多少条文本”和“所有文本合计多少 Token”。Gateway 因此不能只按固定条数切片。

假设：

```text
EMBEDDING_BATCH_SIZE=3
EMBEDDING_MAX_BATCH_TOKENS=100
```

待处理文本 Token 分别为 `40、40、40、10`，实际批次是：

```text
批次 1：40 + 40 = 80
批次 2：40 + 10 = 50
```

虽然第一批还能再放一条，但加入后会达到 120，超过 Token 上限。`makeBatches` 每次同时检查条数和 Token，保证任何供应商请求都不越界。

所有输入会先完成校验，再发送第一批。这样不会处理到一半才发现末尾存在超长文本，减少不必要调用和半完成状态。

## 6. 内容哈希缓存与去重

缓存键包含以下信息的 SHA-256：

```text
Provider + Model + Dimension + Hash(UserID) + Hash(Text)
```

设计原因：

- 文本不直接出现在 Redis key 或日志中；
- 换模型或维度后不会误用旧向量；
- 同一用户重复索引相同内容可以复用；
- 不跨用户共享缓存，避免租户间通过时序或缓存生命周期形成隐私关联。

缓存 Value 只包含向量和维度，不保存原文。读取到损坏数据、维度不一致或 Redis 故障时，Gateway 会记录 Warning 并回退供应商调用；缓存不可用不会阻断核心 Embedding 能力。

同一次请求中的重复文本会先在内存中合并，只向供应商发送一次，再把同一向量复制回多个输入位置。跨请求的重复文本由 Redis 缓存命中。

一个重要取舍是：若多批请求中的后续批次失败，Gateway 不向业务返回部分结果，但已经成功批次的向量仍会写入 Redis。业务重试时可以复用它们，从而避免对已成功内容重复计费；数据库写入仍由下一阶段 Service 在拿到完整结果后一次性执行。

## 7. 哪些错误可以重试

Embedding 请求对相同文本是幂等的，因此 Gateway 可以有限重试：

- HTTP `408`；
- HTTP `429`，并尊重 `Retry-After`；
- HTTP `5xx`；
- 网络错误、`EOF`、`UnexpectedEOF`；
- 单批请求超时，且外层业务 Context 尚未取消。

以下错误不重试：

- 用户取消或业务总 Context 到期；
- 空输入、输入过多、Token 超限；
- 返回数量、index 或维度错误；
- 返回向量包含 `NaN` 或无穷值。

响应结构错误通常来自配置错误或不兼容协议，盲目重试不会改善，还可能重复计费。

当前退避为 `base × 2^attempt`，最多重试 3 次；更复杂的随机抖动、熔断和供应商降级留在 P2 Provider 容错阶段。

## 8. 用量与成本

每次业务调用返回：

- 请求前估算 Token；
- 供应商实际 Input Token；
- `usage` 是否完整上报；
- 供应商请求次数与重试次数；
- Redis 命中数和请求内去重数；
- 纳美元成本及其是否已知。

配置价格的单位为“美元 / 百万输入 Token”。项目仍使用纳美元整数：

```text
cost_nano_usd = input_tokens × usd_per_million_tokens × 1000
```

例如实际使用 10,000 Token，单价为 0.02 美元/百万 Token：

```text
10,000 × 0.02 × 1000 = 200,000 nano USD = 0.0002 USD
```

价格留空或供应商不返回 usage 时，`costKnown=false`。发生重试时，失败尝试可能已经在供应商侧完成并计费，却没有成功返回 usage；此时系统保留成功调用可确认的最低成本，但将 `usageReported=false`、`costKnown=false`，不会把不完整成本冒充精确账单。全部来自缓存时没有供应商调用，因此成本可以确定为 0。

Zap 观测事件名为 `embedding gateway observation`，记录元数据和计数，但绝不记录原始输入文本或向量。

## 9. 环境变量

最小 OpenAI 配置：

```dotenv
EMBEDDING_ENABLED=true
EMBEDDING_PROVIDER=openai
EMBEDDING_BASE_URL=https://api.openai.com/v1
EMBEDDING_API_KEY=<your-key>
EMBEDDING_MODEL=text-embedding-3-small
EMBEDDING_DIMENSION=1536
EMBEDDING_SEND_DIMENSIONS=true
EMBEDDING_TOKENIZER=cl100k_base
```

批次、超时、缓存和价格的完整配置及中文注释见项目根目录 `.env.example`。聊天的 `OPENAI_API_KEY` 不会自动回退给 Embedding：显式分开可以让生产环境使用不同项目、额度和密钥权限，也避免部署者无意中把聊天密钥用于批量文档处理。

当前默认 `EMBEDDING_ENABLED=false`。下一阶段知识库摄取 API 上线后，未启用时应返回明确的“知识库向量服务未配置”，而不是在服务器启动时让普通聊天功能不可用。

## 10. 测试覆盖

```bash
cd backend
go test ./internal/config ./internal/provider ./internal/embeddinggateway
go test ./...
```

测试覆盖：

1. OpenAI-compatible 请求路径、认证、请求体和 usage 解析；
2. 可选 `dimensions` 字段；
3. 429 与 `Retry-After` 错误分类；
4. 按条数和 Token 双限制分批；
5. Provider 乱序结果按 index 还原；
6. Token 超限时供应商调用次数为 0；
7. 用户级缓存隔离与请求内去重；
8. 5xx、网络断流的有限重试；
9. 数量、重复 index、维度和非法浮点值校验；
10. 观测数据不包含用户输入正文。

## 11. 下一阶段

下一阶段实现文档摄取流水线：

```text
上传原文件
  → 创建 KnowledgeDocument(uploaded)
  → 文本提取(extracting)
  → 结构感知切分(chunking)
  → EmbeddingGateway(embedding)
  → 事务写入 Chunk + Vector
  → KnowledgeDocument(ready)
```

重点将从“如何调用向量模型”转到“如何编排数据库、对象存储、文本解析和外部 API”，包括状态机、幂等重试、重新索引以及失败后的补偿清理。
