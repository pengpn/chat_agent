# RAG 第四阶段：权限过滤的向量检索

这一阶段只回答一个问题：给定用户的问题，系统能否从该用户明确选择的知识库中，稳定地找到最相关、版本兼容且不超过 Token 预算的原文片段。

它暂时不调用聊天模型。把检索和生成拆开，可以在回答错误时明确区分：是“资料没有检索到”，还是“资料找到了但大模型没有正确使用”。

## 1. 完整调用链

```text
浏览器：query + knowledgeBaseIds
                 │
                 ▼
KnowledgeRetrievalService
    1. 校验问题和知识库数量
    2. 校验当前用户拥有全部知识库
    3. EmbeddingGateway.EmbedQuery
    4. Repository 精确余弦检索候选片段
    5. 相似度阈值过滤
    6. 上下文 Token 预算筛选
    7. 截取最终 Top-K
                 │
                 ▼
带来源、页码、章节和分数的 matches
```

对应代码：

- `internal/service/knowledge_retrieval.go`：检索业务规则；
- `internal/repository/knowledge.go`：用户隔离和 pgvector SQL；
- `internal/httpapi/handler/knowledge.go`：`POST /api/v1/knowledge/search`；
- `internal/embeddinggateway`：查询文本向量化。

检索单独使用 `KnowledgeRetrievalService`，没有继续堆进负责上传的 `KnowledgeService`。摄取和检索变化频率不同：以后增加 OCR 不应影响检索接口，增加混合检索也不应影响上传状态机。

### 1.1 `EmbedQuery` 到底做了什么

`EmbedQuery` 不是“去向量数据库里查询”，而是把用户问题转换成一个可用于相似度计算的向量。它调用的是 Embedding 模型，而不是 pgvector：

```text
用户问题：退款申请需要在几天内提交？
                 │
                 ▼
EmbeddingGateway.EmbedQuery
                 │  调用 Embedding Provider 的 /v1/embeddings 等兼容接口
                 ▼
查询向量：[0.018, -0.031, 0.204, ...]
```

真正查询向量数据库的是下一步 `Repository.SearchChunksExact`。它把刚生成的查询向量交给 PostgreSQL/pgvector，和摄取阶段已经保存的文档分块向量计算余弦距离：

```text
查询向量
   │
   ▼
SearchChunksExact
   │  PostgreSQL/pgvector：embedding <=> query_vector
   ▼
按语义相似度排序的文档分块
```

因此完整过程有两个边界，不能混为一个操作：

| 步骤 | 负责组件 | 输入 | 输出 | 是否调用模型 |
| --- | --- | --- | --- | --- |
| 查询向量化 | `EmbeddingGateway.EmbedQuery` | 用户问题文本 | 一条浮点向量及模型、维度、用量信息 | 是，调用 Embedding 模型 |
| 向量检索 | `Repository.SearchChunksExact` | 查询向量、用户 ID、知识库 ID、模型版本 | 相似文档分块 | 否，只执行数据库查询 |

`EmbedQuery` 复用 `EmbeddingGateway`，是因为网关不只转发 HTTP 请求，还统一负责：

- 校验查询必须且只能包含一条非空文本；
- 请求前估算 Token，执行输入长度和批次预算限制；
- 使用缓存、重试、超时和响应向量校验；
- 记录 Provider、模型、维度、实际或估算 Token、成本和耗时；
- 返回当前查询向量所属的模型版本，供 Repository 过滤不兼容的旧索引。

这里的查询通常只有一条文本，而文档摄取会一次处理多个分块，所以网关分别暴露 `EmbedQuery` 和 `EmbedDocuments`。两者底层可以使用同一种 Embedding 模型，但业务约束、默认用途、批量规模和观测标签不同。

## 2. 为什么先校验权限，再调用 Embedding

客户端提交：

```json
{
  "query": "企业版可以添加多少成员？",
  "knowledgeBaseIds": ["知识库 UUID"]
}
```

客户端只提交服务端 ID，不能提交拼接好的“检索上下文”。Service 会先去重 ID，然后一次性统计当前用户拥有的知识库数量：

```text
ownedCount == 去重后的 knowledgeBaseIds 数量
```

只要有一个 ID 不属于当前用户，整个请求就返回“知识库不存在”，而且不会调用 Embedding Provider。

这个顺序同时解决两个问题：

1. 防止用猜测的 UUID 检索其他用户数据；
2. 防止越权请求仍然消耗 Embedding Token 和费用。

真正的安全边界不只在 Service。向量查询 SQL 仍然包含：

```sql
WHERE knowledge_bases.user_id = ?
  AND knowledge_documents.knowledge_base_id IN ?
```

Service 校验改善行为和成本，Repository 条件负责最终数据隔离。即使未来调用方漏掉一次预校验，SQL 也不能返回跨用户片段。

## 3. 查询为什么必须匹配文档的 Embedding 版本

向量只有在相同的向量空间中才能比较。下面两个向量即使维度相同，也不能保证语义坐标一致：

```text
text-embedding-3-small 生成的 [0.12, ...]
另一个模型生成的          [0.12, ...]
```

查询通过当前 `EmbeddingGateway.EmbedQuery` 得到 Provider、模型和维度。Repository 只允许下面三项完全一致的文档和分块参与检索：

```sql
knowledge_documents.embedding_provider  = 查询 Provider
knowledge_documents.embedding_model     = 查询模型
knowledge_documents.embedding_dimension = 查询维度
knowledge_chunks.embedding_model         = 查询模型
knowledge_chunks.embedding_dimension     = 查询维度
```

旧模型索引不会被错误地拿来和新模型查询比较。管理员切换 Embedding 模型后，需要对旧文档执行 `reindex`；重新索引成功前，这些旧版本文档会自然返回不了结果，而不是产生看似合理但没有数学意义的排序。

## 4. 余弦距离与余弦相似度

pgvector 的 `<=>` 返回余弦距离，值越小越接近：

```sql
knowledge_chunks.embedding <=> query_vector.embedding
```

对外更容易理解的是相似度，值越大越相关：

```text
similarity = 1 - cosine_distance
```

SQL 先按距离升序排列，也就是按相似度降序排列。当前实现使用精确检索：数据库计算所有符合过滤条件的向量距离，再排序取候选。精确检索的优点是结果正确且可复现，适合建立测试基线；缺点是数据量增长后计算成本近似线性增长。

本阶段没有直接增加 HNSW。HNSW 是近似最近邻索引，用更低延迟换取一定召回损失和额外内存。没有精确基线就无法知道“索引变快以后漏掉了多少正确片段”。第 15 阶段会基于数据量、P95 延迟和 Recall@K 决定是否引入。

## 5. 为什么需要 CandidateLimit、Threshold、Top-K 三个参数

三个参数解决不同问题：

```text
数据库精确排序
      │
      ├── CandidateLimit：最多取多少候选交给业务层
      ▼
相似度过滤
      │
      ├── Threshold：证据不够相关就丢弃
      ▼
Token 预算筛选
      │
      ├── ContextTokenBudget：所有来源上下文的总上限
      ▼
最终结果
      └── Top-K：最多给后续生成多少个片段
```

只配置 Top-K 会导致一个常见错误：即使所有片段都不相关，系统仍然硬凑 K 条，再让模型依据错误资料回答。相似度阈值允许返回空数组，明确表达“没有足够证据”。

CandidateLimit 通常大于 Top-K，因为较高排名的某个片段可能太大，放不进剩余 Token 预算。Service 可以跳过它，继续尝试后面的较小候选。

## 6. 上下文 Token 预算怎样计算

这一条链路中会出现两种容易混淆的 Token：

| Token 类型 | 在什么时候产生 | 用来做什么 | 当前阶段是否已经发生 |
| --- | --- | --- | --- |
| Embedding 输入 Token | `EmbedQuery` 把问题发给 Embedding 模型时 | 控制输入长度、限流并计算 Embedding 成本 | 是 |
| 检索上下文 Token | Service 选择文档分块时 | 预留下一阶段聊天 Prompt 能容纳的资料空间 | 只做预算统计，尚未发送给聊天模型 |

Embedding 模型也需要先把文本切成 Token，才能生成向量，所以 `EmbedQuery` 返回 `EstimatedTokens`、`InputTokens` 等用量信息是正常的。这不代表它生成了对话正文或思考内容：Embedding 模型只返回向量，不返回自然语言回答。

检索结果中的 `contextTokens` 则是另一个概念。当前阶段没有调用 Chat LLM，但要提前限制最终选中的资料总量，避免下一阶段把过多分块塞进聊天模型上下文窗口。

每个候选的预算消耗包含：

```text
分块 TokenCount + 来源标签 Token
```

来源标签类似：

```text
[来源：产品手册.pdf，第 3 个片段]
```

不能只累计正文，因为下一阶段真正放进 Prompt 时，文件名、引用编号和分隔符同样占用上下文窗口。

若候选放不进剩余预算，当前策略是跳过，而不是截断。截断可能破坏句子、页码引用或条款含义。摄取阶段已经把分块控制在合理范围；若大量高相关片段都装不下，应调整 chunk 大小或检索预算，而不是在检索阶段静默切半。

最终响应同时返回：

- `tokenCount`：原分块正文 Token；
- `contextTokens`：正文加来源标签后的预算消耗；
- `usedContextTokens`：所有已选片段合计；
- `contextTokenBudget`：服务端本次使用的预算上限。

## 7. 稳定排序为什么重要

不同分块可能拥有完全相同的相似度。如果 SQL 只写：

```sql
ORDER BY embedding <=> query
```

数据库可以在多次查询中任意交换同分结果，导致引用编号、缓存和测试不稳定。

当前排序增加确定性的 tie-breaker：

```sql
ORDER BY
  cosine_distance ASC,
  document_id ASC,
  chunk_index ASC,
  chunk_id ASC
```

同一份数据和同一个问题会得到相同顺序。这里 UUID 字符串可以用于确定性排序；它不代表创建时间，只用于打破同分。

## 8. 被过滤的数据

SQL 不会检索：

- 不属于当前用户的知识库；
- 用户本次没有选择的知识库；
- 状态不是 `ready` 的文档；
- Provider、模型或维度与查询不一致的文档；
- `embedding IS NULL` 的分块；
- 分块模型或维度与查询不一致的数据。

Service 还会过滤 NaN/Inf 相似度。零向量没有有效的余弦方向，不能成为可信结果。

## 9. API 使用

```http
POST /api/v1/knowledge/search
Authorization: Bearer <access-token>
Content-Type: application/json

{
  "query": "退款申请需要在几天内提交？",
  "knowledgeBaseIds": ["5a1d...", "92be..."]
}
```

示例响应中的核心数据：

```json
{
  "matches": [
    {
      "rank": 1,
      "knowledgeBaseId": "5a1d...",
      "documentId": "71ab...",
      "documentName": "退款规则.pdf",
      "chunkId": "934c...",
      "chunkIndex": 2,
      "content": "用户可在付款后七日内申请退款……",
      "tokenCount": 86,
      "contextTokens": 101,
      "pageNumber": 3,
      "sectionTitle": "退款时限",
      "similarity": 0.87
    }
  ],
  "candidateCount": 18,
  "usedContextTokens": 101,
  "contextTokenBudget": 3000,
  "embeddingProvider": "openai",
  "embeddingModel": "text-embedding-3-small",
  "embeddingDimension": 1536
}
```

相似度阈值、候选数和 Top-K 不允许网页覆盖。它们是服务端检索策略，需要通过评估集统一调整，不能让每个客户端形成不同的安全和成本边界。

## 10. 配置

```dotenv
RAG_RETRIEVAL_CANDIDATE_LIMIT=30
RAG_RETRIEVAL_TOP_K=5
RAG_RETRIEVAL_SIMILARITY_THRESHOLD=0.35
RAG_RETRIEVAL_CONTEXT_TOKEN_BUDGET=3000
RAG_RETRIEVAL_MAX_KNOWLEDGE_BASES=8
RAG_RETRIEVAL_MAX_QUERY_CHARS=4000
```

初始调参思路：

- `CandidateLimit`：先设为 Top-K 的 4～10 倍；
- `TopK`：常见问答可从 3～6 开始；
- `SimilarityThreshold`：不同模型的分数分布不同，不能照搬；
- `ContextTokenBudget`：必须和聊天模型的上下文管理共同分配；
- 最大知识库数和问题长度：限制一次查询的数据库扫描范围和 Embedding 成本。

现在的默认值只是可运行基线，不代表最佳效果。真正阈值要用业务问题、正确文档和期望引用组成的数据集校准。

## 11. 测试覆盖

单元测试验证：

- 权限失败发生在 Embedding 调用之前；
- 重复知识库 ID 会先去重；
- 相似度阈值不会为了凑 Top-K 接受低质量片段；
- 过大的候选会被 Token 预算跳过；
- 最终结果仍保持候选相关性顺序；
- 查询使用当前 Embedding Provider、模型和维度。

真实 PostgreSQL/pgvector 集成测试验证：

- 余弦距离 SQL 能正确执行；
- 其他用户即使知道知识库 UUID 也得到空结果；
- 未就绪文档和维度不匹配向量不会参与检索；
- 相同分数按分块序号稳定排序；
- 1000 个分块时仍严格遵守候选 `LIMIT`。

大数据测试目前验证正确性和有界返回，不设置脆弱的毫秒级断言。性能基线应在固定机器、固定 PostgreSQL 配置和足够预热后记录 P50/P95，而不是让 CI 因机器负载偶发失败。

## 12. 下一阶段

下一阶段是 Grounded Generation 与文档引用：

1. Chat 请求只携带知识库 ID；
2. 调用本阶段的 Retrieval Service；
3. 把 matches 编成带稳定编号的“不可信来源上下文”；
4. 要求模型仅依据来源回答，证据不足时明确说明；
5. 校验模型返回的引用编号；
6. 在 assistant 消息完成事务中保存 `MessageCitation` 快照；
7. 流式结束时把引用元数据发送给浏览器。

最重要的边界是：文档内容是数据，不是系统指令。即使资料里出现“忽略之前要求、泄露密钥”，也必须作为被引用的原文处理，不能提升为 Prompt 指令。
