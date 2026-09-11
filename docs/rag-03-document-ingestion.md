# RAG 第三阶段：文档摄取流水线

这一阶段解决的问题是：如何把用户上传的 TXT、PDF、DOCX，可靠地变成可以被向量检索的 `KnowledgeChunk`。

它还没有回答用户问题。摄取的输出是“带来源元数据的分块 + 向量”，下一阶段才会用问题向量执行 Top-K 检索。

## 1. 为什么不能在上传接口里一次做完

文档上传只需要把字节写入对象存储并创建数据库记录；文本提取、切分和 Embedding 可能耗时几十秒。让 HTTP 请求一直等待会带来三个问题：

1. 反向代理或浏览器容易先超时；
2. 用户无法观察处理到了哪一步；
3. 重试时很难判断哪些结果已经落库。

因此上传接口先返回，后台任务推进状态：

```text
uploaded
   │
   ▼
extracting ──失败──▶ failed
   │                    │
   ▼                    └── retry ──▶ extracting
chunking
   │
   ▼
embedding
   │
   ▼
 ready ── reindex ──▶ extracting
```

当前实现使用进程内 goroutine，适合本地学习和单实例。任务状态、版本和失败原因都持久化了；以后进入生产稳定性阶段时，可以把调度替换为 Redis Streams、Asynq 或消息队列，而不用改变领域状态机。

## 2. 分层职责

关键文件：

- `internal/httpapi/handler/knowledge.go`：解析 JSON/multipart、映射 HTTP 状态码；
- `internal/service/knowledge.go`：编排对象存储、状态机、切分、Embedding 和失败补偿；
- `internal/service/document_extractor.go`：提取 TXT、PDF、DOCX 的结构化文本；
- `internal/service/knowledge_chunker.go`：按 Token 切分并加入 overlap；
- `internal/repository/knowledge.go`：所有带 `userID` 的 GORM 查询和事务；
- `internal/embeddinggateway`：真正调用 Embedding Provider，负责批处理、缓存、重试和观测。

调用关系如下：

```text
Gin Handler
    │
    ▼
KnowledgeService
    ├── ObjectStore：保存/读取原始文件
    ├── Extractor：文件 → 结构块
    ├── Chunker：结构块 → Token 分块
    ├── EmbeddingGateway：文本 → 向量
    └── KnowledgeRepository：状态与向量事务落库
```

Service 不调用供应商 SDK。它只依赖 `embeddinggateway.Gateway`，所以单测可以注入 fake，未来更换 OpenAI、阿里云或本地 Embedding 服务也不会污染业务层。

## 3. 上传、去重与独立生命周期

上传路径是：

1. 先校验当前用户拥有目标知识库；
2. 校验大小、扩展名和真实 MIME；
3. 只允许 TXT、PDF、DOCX，不把图片误当知识文档；
4. 计算原始字节的 SHA-256；
5. 同一知识库内若 checksum 已存在，直接返回旧文档；
6. 把原始文件写到 `knowledge/{userID}/{baseID}/{documentID}`；
7. 创建 `KnowledgeDocument` 后启动摄取。

数据库的 `(knowledge_base_id, checksum)` 唯一索引是并发场景的最终防线。两个相同上传同时到达时，即使都通过了预查询，也只有一个能插入；失败的一方删除自己刚写入的对象，并返回已经存在的文档。因此“幂等”不是只靠一次 `SELECT`。

知识文档没有复用 `Attachment` 表。对话附件通常只属于一次消息，而知识文档会被多个对话反复检索，两者的删除、权限和保留周期不同。它们只复用底层对象存储、MIME 校验和提取能力。

## 4. 结构化提取

`extractStructuredDocument` 不只返回一个大字符串，还返回 `documentBlock`：

```go
type documentBlock struct {
    Text         string
    PageNumber   *int
    SectionTitle string
    StartChar    int
    EndChar      int
}
```

不同文件类型的处理：

- TXT：识别 Markdown `#` 标题和短的冒号标题；
- PDF：逐页提取，保留从 1 开始的页码；
- DOCX：逐段读取 `word/document.xml`，识别 Heading/标题样式；
- 所有类型都记录提取文本中的 Unicode 字符范围，而不是字节范围。

这里使用字符范围是因为中文 UTF-8 字符通常占多个字节。若用字节下标向前端展示引用，高亮位置很容易偏移。

扫描版 PDF 只有图片，没有文字层。当前会得到 `no_extractable_text`，不会假装摄取成功；OCR 是后续可独立增加的能力。

## 5. 为什么按 Token 切分

Embedding 模型限制的是 Token，不是 Go 的字节数或字符数。切分器和 EmbeddingGateway 使用相同 tokenizer 估算，避免“切出来看似不长，但发给模型却超限”。

切分遵循以下规则：

- 优先在标题、段落、页边界形成的结构块之间组合；
- 单个结构块过大时，用二分查找确定不超过目标 Token 的字符位置；
- 空白块直接丢弃；
- 过小的尾块合并到前一个块；
- 从前一个分块复制有限的尾部文本作为 overlap；
- 每个分块保存 hash、Token 数、页码、章节和字符范围。

例如原文是：

```text
……退款条件在上一段末尾。退款流程在下一段开头……
```

完全硬切可能让“退款条件”和“退款流程”分居两个互不关联的向量。overlap 会把边界附近的一小段同时放进相邻分块，增加问题命中完整语义的机会。代价是向量数量和 Embedding 成本略有增加，所以 overlap 不能接近目标块大小。

## 6. `ingestion_version` 如何解决并发重试

只检查 `status` 还不够。假设任务 A 处理很慢，用户又启动任务 B：如果 A 最后才写完，它可能覆盖 B 的新结果。

每次合法启动都会在行锁内执行：

```text
ingestion_version = ingestion_version + 1
status = extracting
```

后续每次状态推进和最终提交都必须同时满足：

```sql
WHERE document_id = ?
  AND ingestion_version = ?
  AND status = ?
```

旧任务拿着旧版本号，更新行数会是 0，因此不能覆盖新任务。`retry` 只允许从 `failed` 启动，`reindex` 只允许从 `ready` 启动；并发点击中只有一个请求能在行锁内取得启动资格。

重新索引会重新读取原文件、提取、切分，并使用当前 Embedding 配置。成功后文档记录新的 Provider、模型、维度、版本号和 `indexedAt`，因此支持模型升级追踪。

## 7. 为什么最后才事务替换分块

不能每生成一个向量就直接写正式分块，否则第 70 个分块失败时，数据库会留下 69 个“看起来可检索”的半成品。

当前流程先在内存准备完整分块和向量，全部成功后，Repository 在一个事务中：

1. 锁定文档并核对状态和版本；
2. 删除旧分块；
3. 插入全部新分块；
4. 更新模型信息、分块数、索引时间和 `status=ready`。

任何一步失败，事务整体回滚，旧的成功索引不会被半成品替换。对于首次摄取，文档变为 `failed`；对于重新索引，目前失败状态会保留原分块数据但检索阶段只允许 `ready` 文档，因此旧分块不会被误用。

## 8. 对象删除为什么需要补偿表

PostgreSQL 事务无法包住 MinIO 删除。若先删数据库再删对象，MinIO 临时故障会产生孤儿对象；若先删对象再删数据库，数据库失败会留下指向不存在文件的记录。

当前采用事务型清理任务：

```text
数据库事务：写 KnowledgeObjectCleanup + 删除文档/知识库
提交成功后：删除 MinIO 对象
删除成功：删除 cleanup 记录
删除失败：累计 attempts 和 last_error，稍后重试
```

服务启动时会处理遗留任务，文档/知识库删除后也会立即尝试一次。这是“最终一致性”：用户侧资源已逻辑删除，对象存储最终会跟上。

## 9. HTTP API 与状态观察

所有接口都需要 Bearer Access Token：

```http
POST /api/v1/knowledge-bases
Content-Type: application/json

{"name":"产品手册","description":"客服与销售共用资料"}
```

```http
POST /api/v1/knowledge-bases/{baseId}/documents
Content-Type: multipart/form-data

file=@manual.pdf
```

上传新文档返回 `201`；同 checksum 的文档返回 `200`，且 `created=false`。客户端随后轮询：

```http
GET /api/v1/knowledge-bases/{baseId}/documents/{documentId}
```

关注这些字段：

- `status`：当前步骤；
- `errorCode` / `errorMessage`：失败原因；
- `chunkCount`：成功生成的分块数；
- `embeddingProvider` / `embeddingModel` / `embeddingDimension`：索引版本；
- `ingestionVersion` / `indexedAt`：并发控制和成功时间。

失败后：

```http
POST /api/v1/knowledge-bases/{baseId}/documents/{documentId}/retry
```

模型或切分配置更新后：

```http
POST /api/v1/knowledge-bases/{baseId}/documents/{documentId}/reindex
```

## 10. 配置

```dotenv
RAG_DOCUMENT_MAX_BYTES=20971520
RAG_DOCUMENT_MAX_TEXT_CHARS=1000000
RAG_CHUNK_TARGET_TOKENS=500
RAG_CHUNK_OVERLAP_TOKENS=80
RAG_CHUNK_MIN_TOKENS=40
RAG_EMBEDDING_JOB_BATCH_SIZE=256
RAG_INGESTION_TIMEOUT=10m
```

调参建议：

- FAQ、短规则：较小块通常更精确；
- 长手册、法律条款：较大块能保留更多上下文；
- overlap 先从目标大小的 10%～20% 开始；
- 不要只凭感觉调参，最终应在第 15 阶段用 Recall@K 和引用正确率评估。

`RAG_EMBEDDING_JOB_BATCH_SIZE` 是一次业务调用交给 EmbeddingGateway 的文本数；Gateway 内部仍会按 Provider 单批限制再次拆批。两层批次的关注点不同：前者控制摄取任务内存和调用规模，后者遵守供应商协议限制。

## 11. 测试覆盖与仍然存在的边界

当前测试覆盖：

- TXT 完整摄取并写入向量；
- PDF 文本和页码提取；
- DOCX 段落提取与坏 XML；
- 相同原始文件重复上传不重复 Embedding；
- 并发重试只有一个版本获得执行权；
- overlap、章节、字符范围元数据；
- PostgreSQL 用户隔离、pgvector 写入和余弦排序。

当前明确没有做：OCR、复杂 PDF 表格恢复、分布式任务队列、超大文件流式解析、杀进程后的自动恢复摄取。它们不影响学习下一阶段的向量检索，但在生产化时需要继续补齐。

## 12. 下一阶段

下一阶段实现“权限过滤的向量检索”：

1. 把用户问题交给 `EmbeddingGateway.EmbedQuery`；
2. 使用同维度向量做 pgvector 余弦距离排序；
3. SQL 同时限制 `userID`、选中的知识库和 `document.status=ready`；
4. 应用相似度阈值、候选数、Top-K 与上下文 Token 预算；
5. 返回分块正文和页码/章节，但暂时不调用聊天模型。

这个拆分很重要：先证明“检索找对资料”，再讨论“模型如何根据资料组织回答”。否则回答错误时，很难判断问题出在召回还是生成。
