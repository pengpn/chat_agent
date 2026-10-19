# RAG 第七阶段：离线评估与进阶检索决策

本阶段的核心问题是：一条错误回答，到底错在「没找到资料」，还是「资料找到了但模型没用好」？我们为检索和生成分别记录证据与指标，不靠肉眼看几条对话就调参数。

后续延伸：[第八阶段](./rag-08-hybrid-retrieval-and-rrf.md)已提供默认关闭的关键词 + 向量 RRF 实验能力，可用本节的真实快照流程对比；默认策略仍需由真实评估结果决定。

[第九阶段](./rag-09-reranker-gateway-and-evaluation.md)增加可配置的重排网关和分数重放，继续沿用本节的数据集与指标。

## 1. 数据流和代码入口

```text
真实知识库 + 标注问题
       │
       ├── cmd/rageval capture ──► 候选快照 JSON（真实 Embedding + 精确 pgvector）
       │                            包含 query / 候选片段 / 分数 / 耗时 / 模型版本
       │
       ├── 人工补全回答、事实断言及引用标注
       │
       └── cmd/rageval evaluate ─► 各指标和逐题诊断
                         sweep ─► 相同候选下的 Top-K × 阈值重放
```

`KnowledgeRetrievalService.SearchWithTrace` 与在线 `Search` 使用同一条检索和筛选路径。线上正常调用仍返回原有 `matches`；调试快照不进入 HTTP 响应。`service.SelectKnowledgeCandidates` 是共享的筛选函数，离线重放使用记录时相同的 Embedding tokenizer 来计算来源开销，避免出现「评估脚本和线上逻辑各写一份」的偏差。

关键文件：

- `backend/internal/service/knowledge_retrieval.go`：实际检索、逐候选决策、耗时。
- `backend/internal/rageval/eval.go`：数据集校验、指标和失败分类。
- `backend/cmd/rageval/main.go`：抓取真实候选、评估、参数扫描。
- `backend/eval/rag/`：**合成示例**，仅用于验证工具和学习，不能代表本项目真实效果。

## 2. 先给问题标注，再运行实验

`Dataset` 中每题包含 `query`、`goldAnswer`、相关文档及 1～3 级相关性、期望引用的文档 ID。标注以**文档 ID**为单位：改变分块大小后，chunk ID 会变，但源文档仍可比较。空的 `relevantDocuments` 表示「资料里没有答案」，用于测试系统是否诚实拒答。

制作正式评估集时，从真实业务问题抽样，分别覆盖：直白问法、同义改写、跨段落、多个文档、含数字/专有名词、无答案、文档中的恶意提示。答案和引用应由人审阅，不能直接用当前模型输出当标准答案。固定 `datasetId` 和 `corpusVersion`，修改标注时升版本。

`capture` 读取项目 `.env`，使用指定用户自己的知识库进行权限校验，然后调用真实 EmbeddingGateway 和 pgvector。数据集里的 `knowledgeBaseIds`、`relevantDocuments` 必须替换成当前库的真实 UUID；命令不会创建文档或修改索引。输出含原文片段，应保存在受控目录，不提交用户数据。

```bash
cd backend
go run ./cmd/rageval capture \
  -dataset /path/to/private-dataset.json \
  -user-id <当前用户UUID> \
  -name exact-vector-k5 \
  -out /path/to/private-baseline.json

go run ./cmd/rageval evaluate \
  -dataset /path/to/private-dataset.json \
  -runs /path/to/private-baseline.json \
  -out /path/to/private-report.json
```

快照记录 `query`、候选片段全文、原始余弦分数、候选/最终片段 ID、Embedding Provider/模型/维度/tokenizer、Prompt 版本、Embedding/数据库检索耗时。`corpusFingerprint` 由源文档 ID 与校验和组成：同样资料重新分块仍可对比，换资料时拒绝把两份实验直接放在一起。`indexFingerprint` 由就绪文档的摄取版本、索引时间、分块数和向量模型组成，用来发现索引是否真的发生变化。`capture` 用独占创建写文件，避免覆盖已完成人工标注的快照。

线上另有 `RAG_RETRIEVAL_TRACE_ENABLED=false`。仅在受控调试时改为 `true`；它会将 query 和候选原文写到 Zap 日志，可能包含敏感信息。修改配置时 `.env` 与 `.env.example` 已同步。

## 3. 四类指标分别看什么

| 指标 | 算法 | 解释 |
| --- | --- | --- |
| Candidate Recall | 有相关文档进入向量候选池的比例 | 候选阶段漏掉了多少资料 |
| Recall@K | 最终入上下文的相关文档数 / 标注相关文档数 | 阈值、Top-K、预算之后还有多少证据 |
| MRR@K | 首个相关文档名次的倒数 | 正确资料是否靠前 |
| nDCG@K | 按 1～3 级相关性折损名次，再除以理想排序 | 多级相关文档的排序质量 |
| Faithfulness | 人工判定有来源支持的事实断言数 / 全部事实断言数 | 回答是否编造或歪曲 |
| Citation Precision | 被引用且属于期望与实际选中来源的文档 / 全部被引用文档 | 引用是否乱指 |
| Citation Recall | 正确引用的文档 / 期望引用文档 | 应给的出处是否给全 |
| Answer Accuracy | 人工判定回答正确的题目比例 | 忠实但答非所问也会被发现 |

检索的宏平均只统计有答案的题。无答案题单独报告 `noEvidenceAccuracy`：没有误选片段的比例。多个 chunk 来自同一文档时，排序指标按文档去重，避免 overlap 重复片段把同一资料算成多次命中。

回答忠实度**不能由引用编号或答案字符串自动证明**。`capture` 输出只有检索数据。人工复核回答后，在参数中填入 `generationProvider`、`generationModel`，并在对应 case 填入 `answer`、`answerCorrect`、`claims`、`citedDocumentIds` 和可选的 `generationMs`。每个 claim 的 `text` 必须出现在回答中，`supported` 应根据该次实际选中的来源判断；缺少复核时报告只提供检索指标，并显示 `generationGraded=0`。期望引用来自事先标注；实际引用按回答里的 `[n]` 映射到本次选中的文档，再与记录的 `citedDocumentIds` 对照，不信任手填 ID。不存在的编号会拉低引用精确率。

逐题 `outcome` 用来快速定位：

- `candidate_miss`：相关文档连候选池都没进。先检查切分、Embedding 和检索方式。
- `selection_miss`：候选里有相关文档，但被阈值、Top-K 或预算筛掉。先看候选决策。
- `no_evidence_false_positive`：无答案题却选中了不相关的资料。检查阈值与拒答行为。
- `generation_or_citation_error`：相关资料已经入上下文，但答案、事实或引用有误。先看 Prompt、模型和引用链。
- `generation_ungraded`：检索已有结果，回答尚未人工复核。
- `pass`：检索和已复核的回答都通过。

## 4. 复现实验，而不是凭感觉调参

先固定数据集、源文件、Embedding 模型和候选池。`sweep` 在同一批候选上重新运行生产筛选函数：

```bash
go run ./cmd/rageval sweep \
  -dataset /path/to/private-dataset.json \
  -run /path/to/private-baseline.json \
  -top-k 3,5,8 \
  -threshold 0.25,0.35,0.45 \
  -out /path/to/private-sweep.json
```

扫描会清除旧回答及人工标签，因为上下文一变，旧答案的忠实度结论就不能复用。比较时同时看 Recall@K、nDCG@K、无答案误选率、上下文 Token 与延迟，不能只追求更大的 Top-K。

改变 **chunk 大小或 overlap** 必须按该配置重新索引相同源文档，再次 `capture`；只改快照的参数名没有意义。改变 **Prompt** 必须让同一问题重新生成并重新复核 claims。每个快照保留所有参数、模型和版本，再用 `evaluate -runs baseline.json,variant.json` 对比。工具拒绝对比源文档指纹不同的运行；源文档相同并不证明两次 chunk 版本相同，所以还要核对参数和 `IndexedAt`/摄取版本记录。

仓库提供的 `demo-dataset.json`、`demo-baseline.json`、`demo-top3.json` 用合成分数演示：增加 Top-K 后，示例的 Recall@K 可能上升，但一题仍然 `candidate_miss`，另一题依旧 `generation_or_citation_error`。这些数字不用于生产决策。

## 5. 什么时候评估进阶检索

目前本机库在 2026-10-10 的只读检查结果为 **3 个知识库、1 份就绪文档、2 个带向量分块**。这不足以比较策略或测 HNSW 收益。先积累可人工复核的真实问题、跨文档答案和无答案样本；之后在相同源文档和查询集上分别抓取以下候选快照：

1. **关键词 + 向量混合**：针对专有名词、产品型号、条款编号的漏召回；保留两路候选与融合分数，比较 Candidate Recall、nDCG 和延迟。
2. **Reranker**：只有向量候选已包含正确文档、但排序靠后时才有价值。它无法救回根本未进入候选池的文档，且会增加延迟和成本。
3. **查询改写**：针对模糊指代或同义表达的漏召回；必须保留原 query、改写 query 和额外模型用量，防止改写改变用户意图。
4. **HNSW**：精确检索出现可重复的延迟瓶颈后，用同一数据集比较近似索引的 Recall@K、p50/p95 延迟、磁盘和构建成本，再决定是否启用。当前不建索引。

这几项目前是**待用真实数据验证的候选方案**，不是已取得收益的功能。pgvector 官方说明默认精确检索具有完全的近邻召回；近似索引以部分召回换速度，并且过滤条件可能进一步影响结果。RAGAS 论文也把检索相关性、回答忠实度和回答质量分开讨论。参考：[pgvector 官方文档](https://github.com/pgvector/pgvector)、[RAGAS 论文](https://arxiv.org/abs/2309.15217)。

## 6. 验证

```bash
cd backend
go test ./internal/service ./internal/rageval ./internal/config
go run ./cmd/rageval evaluate \
  -dataset eval/rag/demo-dataset.json \
  -runs eval/rag/demo-baseline.json,eval/rag/demo-top3.json
```

单测覆盖检索逐候选决策、指标聚合、无答案题分母、失败归因、引用精确率、人工标注约束和不同语料拒绝对比。真实检索快照需要项目自己的 Embedding 配置和已索引文档；仓库不会提交真实用户资料。
