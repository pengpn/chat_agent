# RAG 第八阶段：关键词召回与 RRF 混合检索

这一节实现一个可以与精确向量检索对比的混合检索实验。向量善于按语义寻找资料；关键词提供另一条独立召回路径，帮助查找型号、编号和文档里的原词。两路结果按片段 ID 去重，再用 RRF 合并排名。

## 1. 请求怎样流动

```text
问题 + 当前用户 + 所选知识库
             │
             ├── 权限校验
             ├── EmbeddingGateway.EmbedQuery（一次）
             │
             ├── pgvector 精确向量召回 ──► vectorRank / similarity
             └── PostgreSQL 关键词召回 ─► keywordRank / keywordScore
                                      │
                                      ▼
                               RRF 融合并按片段去重
                                      │
                                      ▼
                           Top-K + 上下文 Token 预算
                                      │
                                      ▼
                       原有 Grounded Generation 与引用快照
```

没有增加新的模型调用：仍然只生成一次查询向量，关键词检索在 PostgreSQL 内执行。两路查询分别应用 `userID`、知识库范围、文档 `ready` 状态、Embedding Provider/模型/维度和非空向量过滤。关键词路径不会绕过权限和文档生命周期。

主要代码：

- `backend/internal/knowledgelex/tokenizer.go`：中文二元切词、英文数字词、查询词上限和规则版本。
- `backend/internal/repository/knowledge_keyword.go`：关键词索引、旧数据回填和 GORM 查询。
- `backend/internal/service/knowledge_hybrid.go`：片段去重、RRF 公式和稳定排序。
- `backend/internal/service/knowledge_retrieval.go`：模式选择、两路召回、统一上下文预算及 Trace。
- `backend/internal/rageval/eval.go`：按记录的原始名次重建 RRF，再用线上筛选函数重放。

## 2. 中文为什么需要先分词

本实现先把文本整理成以空格分隔的检索词，再交给 PostgreSQL 的 `simple` 配置。连续中文使用相邻两个字作为一个词：

```text
申请退款  →  申请 / 请退 / 退款
Qwen3-VL-Flash  →  qwen3 / vl / flash / qwen3vlflash
```

文档和问题使用同一套规则。问题中的词去重后最多保留 64 个，过滤少量疑问词和英文停用词。查询词用 OR 连接，以便召回候选；用户输入的引号、括号、`!`、`&` 等不会成为全文检索运算符。SQL 始终使用参数传值。

这是一套便于学习、无需额外词典服务的轻量规则。中文跨词边界可能产生噪声，单字查询、同义词和拼写纠错能力有限；当前没有语义分词或词干还原。效果要通过实际问题集验证。

`KnowledgeChunk` 新增内部字段 `keyword_text` 和 `keyword_version`。Repository 在分块落库时计算它们，启动迁移则对旧片段分批回填，随后建立：

```sql
CREATE INDEX knowledge_chunks_keyword_gin_idx
ON knowledge_chunks USING GIN (to_tsvector('simple', keyword_text));
```

查询通过 `@@` 判断命中，使用 `ts_rank(..., 32)` 排序。这是 PostgreSQL 全文检索排名，**不是 BM25**。GIN 的表达式与查询表达式保持一致，便于数据库使用索引。[PostgreSQL 全文检索索引文档](https://www.postgresql.org/docs/16/textsearch-indexes.html)、[排名函数文档](https://www.postgresql.org/docs/16/textsearch-controls.html)。

回填只处理旧版本，不修改向量，也不重新调用 Embedding。只有符号的片段也会更新版本，避免下次启动反复处理。当前沿用项目的开发期启动迁移；数据量变大后，应在版本化 Migration 阶段处理回填和索引构建窗口。

## 3. RRF 的 Go 代码在算什么

每个来源列表中的名次从 1 开始。某片段在一个列表中出现，就贡献一次：

```text
RRF(chunk) = Σ 1 / (k + rank)
```

默认 `k=60`。假设片段 A 的向量名次是 1，但未命中关键词；片段 B 的向量名次是 2，关键词名次是 1：

```text
A = 1 / 61             ≈ 0.01639
B = 1 / 62 + 1 / 61    ≈ 0.03252
```

B 因为两路都支持而排到前面。关键词原始分数和余弦相似度不在同一量纲，RRF 使用名次避免直接相加；`k` 控制名次优势衰减程度。[RRF 公式与说明](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/reciprocal-rank-fusion)。

`FuseKnowledgeCandidates` 按 `ChunkID` 去重，每路最多贡献一次；分数相同时按文档 ID、片段序号、片段 ID 稳定排序。每路最多取 `CandidateLimit` 个，融合后的完整并集最多是它的两倍，Trace 和评估快照保留全部候选，不截断掉某一路的信息。

`similarity` 始终保留原始余弦相似度；`keywordScore` 保留全文检索分数；`fusionScore` 表示融合排名分数。它们都不能直接解释为答案正确概率。

## 4. 阈值和 Token 预算如何应用

纯向量模式保持原有语义：低于余弦阈值的片段被过滤。

混合模式下，**只有向量命中的片段**仍受余弦阈值约束；有真实关键词命中的片段可以通过关键词路径进入上下文，即使其余弦分数较低。否则，关键词路径救回的候选还会被同一个余弦阈值删除，混合召回便失去意义。

这也可能带来关键词误命中的噪声。因此要同时观察无答案题的 `noEvidenceAccuracy`。所有路径仍统一受最终 Top-K 和上下文 Token 预算限制；超过预算的片段不会进入生成请求。关键词数据库查询失败时返回检索错误，不把一次失败的混合实验默认为成功的向量实验。

## 5. 如何开启与回退

`.env` 和 `.env.example` 已同步，默认保持向量基线：

```dotenv
RAG_RETRIEVAL_MODE=exact_vector
RAG_RETRIEVAL_RRF_K=60
```

启动一次新版后端完成关键词字段回填与 GIN 索引创建。要试验混合检索，将 `RAG_RETRIEVAL_MODE` 改为 `hybrid_rrf` 并重启后端。改回 `exact_vector` 即可回到基线，无需删除数据或重新生成向量。

## 6. 怎样用上一节的工具对比

用相同源文档、Embedding 模型、问题集和参数分别抓取：

```bash
cd backend
RAG_RETRIEVAL_MODE=exact_vector go run ./cmd/rageval capture \
  -dataset /path/to/private-dataset.json -user-id <用户UUID> \
  -name vector-baseline -out /path/to/vector.json

RAG_RETRIEVAL_MODE=hybrid_rrf go run ./cmd/rageval capture \
  -dataset /path/to/private-dataset.json -user-id <用户UUID> \
  -name hybrid-rrf -out /path/to/hybrid.json

go run ./cmd/rageval evaluate -dataset /path/to/private-dataset.json \
  -runs /path/to/vector.json,/path/to/hybrid.json
```

新快照包含两路名次、关键词分数、融合分数、规则版本、RRF 常数及关键词查询耗时。重放时根据两路名次重新计算 RRF，而不是直接信任保存的融合分数。比较 Recall@K、MRR、nDCG、无答案误选率和额外数据库耗时；要判断回答是否改善，还需重新生成并人工复核答案。

仓库提供 `demo-hybrid.json` 演示关键词找回向量候选池遗漏的文档。它的分数是合成数据，未调用真实模型，也没有替混合检索填写回答质量标签：

```bash
go run ./cmd/rageval evaluate -dataset eval/rag/demo-dataset.json \
  -runs eval/rag/demo-baseline.json,eval/rag/demo-hybrid.json
```

## 7. 本节测试与下一节

单测覆盖中文与型号分词、查询运算符处理、长输入上限、RRF 去重和稳定排序、关键词单路召回、默认模式不查询关键词，以及混合快照重放。PostgreSQL 集成测试覆盖中文独立命中、跨用户隔离、模型版本过滤、未就绪文档过滤和旧片段回填。

下一节可以学习 Reranker：当正确片段已经进入候选池、但顺序不理想时，对候选重新排序。它的效果仍需要同一个评估集证明；真实业务集没有证明收益前，保持本节实验开关的默认值。
