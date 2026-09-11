# RAG 第一阶段：领域模型与 pgvector 基础

本文对应 `TODO.md` 的“9. 知识库领域模型与 pgvector 基础”。这一阶段的目标不是立即做出完整 RAG，而是先建立不会被后续 Embedding、检索和引用需求推翻的数据边界。

完成本阶段后，系统具备：

- 用户独立拥有的知识库、知识文档和文档分块；
- PostgreSQL `vector` 扩展及 Go 向量类型映射；
- 每次数据库访问都显式校验用户所有权的 Repository；
- 回答引用的来源快照，原文删除后仍能解释历史答案；
- 向量存取、余弦排序、用户隔离和删除语义的 PostgreSQL 集成测试。

本阶段明确**没有**实现：知识库 HTTP API、文件摄取任务、文本切分算法、Embedding API 调用、聊天检索和前端引用面板。这样拆分是为了先验证数据与权限，再逐层增加外部调用和业务复杂度。

## 1. RAG 的完整链路

RAG 是 Retrieval-Augmented Generation，即“检索增强生成”。一次完整调用通常经过：

```text
文档上传 → 文本提取 → 文本分块 → 生成 Embedding → 保存向量
                                               ↓
用户问题 → 问题 Embedding → 相似度检索 → 组织证据 → LLM 回答 → 保存引用
```

第一行是离线或异步的“摄取链路”，第二行是用户提问时的“查询链路”。二者共享 Embedding 模型和向量空间，但错误处理、延迟目标与重试策略完全不同，因此后续不应写进一个巨大的 Service 方法。

## 2. 为什么知识库不能等同于对话附件

当前项目已有 `Attachment`。它服务于某一轮对话：文件被消息认领，并随会话或账号生命周期清理。知识库则是用户可长期维护、跨多个会话重复选择的资料集合。

如果直接复用 Attachment 作为知识库，会产生几个问题：

- 文档到底属于消息还是知识库，生命周期不清晰；
- 同一文档在多个会话复用时容易重复解析和向量化；
- 删除某个会话可能误删其他会话依赖的资料；
- 很难表达知识库级权限、重新索引和 Embedding 版本。

因此项目单独增加 `KnowledgeBase`、`KnowledgeDocument` 和 `KnowledgeChunk`，只复用底层对象存储及文本提取能力，不复用对话附件的所有权关系。

## 3. 数据模型

```mermaid
erDiagram
    USER ||--o{ KNOWLEDGE_BASE : owns
    KNOWLEDGE_BASE ||--o{ KNOWLEDGE_DOCUMENT : contains
    KNOWLEDGE_DOCUMENT ||--o{ KNOWLEDGE_CHUNK : splits_into
    MESSAGE ||--o{ MESSAGE_CITATION : has
    KNOWLEDGE_BASE o|--o{ MESSAGE_CITATION : referenced_by
    KNOWLEDGE_DOCUMENT o|--o{ MESSAGE_CITATION : referenced_by
    KNOWLEDGE_CHUNK o|--o{ MESSAGE_CITATION : referenced_by

    KNOWLEDGE_DOCUMENT {
        uuid id
        text object_key
        string status
        text extracted_text
        string embedding_model
        int embedding_dimension
    }

    KNOWLEDGE_CHUNK {
        uuid id
        int chunk_index
        text content
        int token_count
        vector embedding
        jsonb metadata
    }

    MESSAGE_CITATION {
        uuid message_id
        uuid nullable_chunk_id
        int ordinal
        text source_name
        int nullable_page_number
        text quote
        float similarity_score
    }
```

### KnowledgeBase

知识库是用户拥有的顶层集合，例如“公司制度”“产品手册”“法律案例”。它不绑定任何会话，因此同一个知识库后续可以被多次选择。

### KnowledgeDocument

文档保存来源元数据和摄取状态：

- 原文件字节放在 MinIO/S3，数据库只保存 `object_key`；
- `checksum` 用于检测同一知识库中的重复内容；
- `extracted_text` 是内部处理中间产物，不通过 JSON 导出；
- `embedding_provider`、`embedding_model`、`embedding_dimension` 记录向量版本；
- `status` 为后续异步流水线提供明确状态，而不是只用一个布尔值。

状态机预留为：

```text
uploaded → extracting → chunking → embedding → ready
     └──────────── 任一步失败 ───────────────→ failed
```

后续实现 Service 时，状态只能按允许的方向迁移；失败应保存稳定的错误码和可读错误信息，重试时从安全边界重新开始。

### KnowledgeChunk

Chunk 是最小检索单元。除正文外还保留：

- `chunk_index`：恢复原始顺序；
- `content_hash`：缓存 Embedding、判断内容是否变化；
- `token_count`：检索后组织上下文时执行 Token 预算；
- 页码、章节、JSON 元数据：回答引用可以定位原文；
- Embedding 模型和维度：避免不同向量空间的数据被混在一起比较。

当前 `embedding` 使用不限制维度的 `vector`，因为下一阶段才会确定部署的 Embedding 模型。这样便于教学和切换模型，但也意味着不能直接在该列上创建固定维度的 HNSW 索引。生产方案通常会固定一个向量维度，或按模型版本拆分索引/表。

### MessageCitation

只保存 `chunk_id` 不足以支撑长期历史：用户删除知识库时，外键级联会让来源消失。这里同时保存 `source_name`、页码、章节、引用原文和当时的相似度分数，并让知识来源外键可空、删除时 `SET NULL`。

结果是：

- 删除消息时，引用随消息级联删除；
- 删除知识库或文档时，分块删除，但历史消息引用快照保留；
- 引用仍能解释“当时的回答依据是什么”，但不能再跳转到已删除来源。

这是一种有意的数据取舍。若产品要求“删除文档后历史回答也必须擦除引用原文”，则隐私策略优先，应同步清理快照。

## 4. pgvector 与距离运算

项目把 Compose 中的 PostgreSQL 镜像切换为 `pgvector/pgvector:0.8.6-pg16`，仍保持 PostgreSQL 16 主版本，并在迁移开始时执行：

```sql
CREATE EXTENSION IF NOT EXISTS vector;
```

Go 侧使用 `github.com/pgvector/pgvector-go` 将 `[]float32` 映射为 PostgreSQL `vector`。当前选择 v0.3.0，是因为项目保持 Go 1.23 模块兼容；该库更新版本的 Go 版本要求更高。

pgvector 常用距离操作符：

| 操作符 | 含义 | 越小是否越相似 |
| --- | --- | --- |
| `<->` | L2 欧氏距离 | 是 |
| `<#>` | 负内积 | 是 |
| `<=>` | 余弦距离 | 是 |
| `<+>` | L1 距离 | 是 |

测试使用余弦距离：

```sql
SELECT *
FROM knowledge_chunks
WHERE document_id = $1
ORDER BY embedding <=> $2
LIMIT 1;
```

注意返回的是“距离”，不是“相似度”。余弦相似度通常可表示为 `1 - cosine_distance`。命名字段或阈值时必须明确存的是哪一个，避免把大小关系写反。

本阶段先使用精确检索。它速度不是最终目标，却能给后续 HNSW、混合检索或 Reranker 提供正确性基线。没有基线就无法判断索引参数带来了多少召回损失。

官方资料：

- [pgvector：PostgreSQL 向量相似度搜索扩展](https://github.com/pgvector/pgvector)
- [pgvector-go：Go/GORM 类型与查询集成](https://github.com/pgvector/pgvector-go)

## 5. Repository 为什么每个方法都带 userID

UUID 难猜不等于权限控制。若方法只有 `FindDocumentByID(documentID)`，Service 很容易忘记再校验文档所属用户，最终形成 IDOR（越权读取）漏洞。

`KnowledgeRepository` 的读取和修改方法显式接收 `userID`，查询通过知识库联表或子查询完成所有权过滤。例如：

```sql
SELECT knowledge_documents.*
FROM knowledge_documents
JOIN knowledge_bases
  ON knowledge_bases.id = knowledge_documents.knowledge_base_id
WHERE knowledge_documents.id = $documentID
  AND knowledge_bases.user_id = $userID;
```

不存在和不属于当前用户都映射为 `domain.ErrNotFound`。这既减少调用方遗漏，也避免通过 403/404 差异探测其他用户资源是否存在。

接口与 `GormKnowledgeRepository` 实现放在同一个 `repository/knowledge.go`，符合本项目约定：调用侧依赖小接口，持久化细节集中在 Repository，学习时也能在一个文件里看到契约和实现。

## 6. ReplaceChunks 为什么使用事务

重新切分或重新生成 Embedding 时，旧分块和新分块必须作为一个整体替换：

1. 锁定属于当前用户的文档；
2. 删除旧分块；
3. 批量写入新分块；
4. 更新文档的 `chunk_count`；
5. 任一步失败则整体回滚。

如果不用事务，第三步失败可能留下“旧分块已删、新分块只写了一半”的文档。后续摄取 Service 还要在事务外协调对象存储和外部 Embedding API，因为数据库事务无法回滚外部系统，需要幂等键与补偿任务。

## 7. 账号导出和删除边界

知识库属于用户数据，因此账号导出增加知识库、文档、分块和消息引用；密码、Token 哈希、对象存储键、内部提取全文和向量不进入 JSON。

账号注销前，`ListObjectKeysByUser` 同时收集对话附件和知识文档对象键，Service 可以先清理 MinIO，再删除用户，让数据库外键级联清理关系数据。

本阶段尚无知识库删除 Service，所以直接调用 Repository 删除知识库只处理数据库记录，不会删除 MinIO 原文件。第 11 阶段实现摄取与 HTTP API 时，必须补上对象存储补偿和孤儿清理任务。

## 8. 本地升级与验证

首次启动或从原 `postgres:16-alpine` 升级时执行：

```bash
docker compose pull postgres
docker compose up -d postgres
```

镜像仍为 PostgreSQL 16，并继续挂载现有 `postgres_data` volume；正式数据升级前仍建议先备份。应用启动时会自动执行 `CREATE EXTENSION vector` 和 GORM AutoMigrate。

后端完整测试：

```bash
cd backend
go test ./...
```

Repository 的 PostgreSQL 集成测试默认跳过，显式提供测试数据库后运行：

```bash
TEST_DATABASE_URL='postgres://agent:agent_dev_password@localhost:5432/agent_platform?sslmode=disable' \
  go test ./internal/repository -run TestPostgresKnowledgeOwnershipVectorAndCitationSnapshot -count=1
```

集成测试验证四件事：

1. 其他用户不能向不属于自己的知识库写文档或替换分块；
2. 向量可以通过 GORM 写入和读回；
3. `<=>` 能把最接近查询向量的分块排在第一位；
4. 删除知识库后，引用外键变空但引用快照仍然存在。

## 9. 下一阶段怎么学

下一步实现 `EmbeddingGateway`，建议沿用 `LLMGateway` 的“业务入口不直连供应商”思想，但不要直接把 Embedding 塞进聊天 Gateway：

```go
type EmbeddingClient interface {
    Embed(ctx context.Context, texts []string) (EmbeddingResponse, error)
}

type EmbeddingGateway interface {
    EmbedDocuments(ctx context.Context, request EmbedRequest) (EmbedResult, error)
    EmbedQuery(ctx context.Context, request EmbedRequest) (EmbedResult, error)
}
```

重点先学会批量大小、Token 上限、维度校验、内容哈希缓存、超时重试和成本记录。只有这层稳定后，文档摄取状态机才有可靠的外部能力可以编排。
