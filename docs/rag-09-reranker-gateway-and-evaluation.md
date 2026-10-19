# RAG 第九阶段：Reranker 网关、候选重排与离线评估

这一节解决「正确片段已经召回，但顺序靠后」的问题。重排模型同时查看问题和每个候选的正文，返回相关性分数；服务端据此调整候选顺序，再执行原有 Top-K 和 Token 预算筛选。

例如，向量检索把通用售后说明排在退款条款之前。Top-K=1 时，模型只能看到通用说明。重排有机会把退款条款提到第一位。若退款条款根本没有进入候选池，重排仍然无法给它打分。

## 1. 它在当前代码中处于哪里

```text
当前用户 + 问题 + 知识库 ID
             │
             ▼
        权限校验 + 查询向量
             │
             ▼
   精确向量召回 / 两路召回与 RRF
             │
             ▼
  RerankGateway：预算 → 供应商调用 → 校验
             │
             ▼
    按分数重排全部原始候选片段
             │
             ▼
   原有证据门槛 + Top-K + Token 预算
             │
             ▼
     生成回答、分配引用、保存快照
```

重排是独立开关，因此支持 `exact_vector + rerank` 和 `hybrid_rrf + rerank` 两种组合。权限先于模型调用；传给重排接口的所有正文都来自当前用户已召回的资料。关闭开关时不会调用重排接口，候选为空时也会跳过调用。

| 文件 | 作用 |
| --- | --- |
| `internal/config/rerank.go` | 独立的模型端点、预算和超时配置 |
| `internal/provider/rerank.go` | 兼容 HTTP 协议适配，解析索引、分数和可选用量 |
| `internal/rerankgateway/gateway.go` | 请求前预算、取消/超时、完整排列校验和 Zap 观测 |
| `internal/service/knowledge_rerank.go` | 将模型索引映射回原始片段，稳定排序 |
| `internal/service/knowledge_retrieval.go` | 在召回后、上下文筛选前执行重排 |
| `internal/rageval/eval.go` | 使用已记录分数重放排序和计算指标 |

生产入口 `cmd/api` 和评估入口 `cmd/rageval` 都组装同一个 RerankGateway。Service 通过接口调用网关，不直接构造供应商客户端。Embedding、Rerank、Chat 是不同的模型任务，各自使用相应网关。

## 2. 兼容接口怎样工作

本节先支持以下请求契约，Endpoint 配置为完整地址，不自动追加路径：

```json
{
  "model": "部署的重排模型名",
  "query": "退款期限是多少？",
  "documents": ["通用售后说明……", "退款须在七天内申请……"],
  "top_n": 2
}
```

响应用输入索引关联原文：

```json
{
  "results": [
    {"index": 1, "relevance_score": 0.95},
    {"index": 0, "relevance_score": 0.20}
  ]
}
```

索引从 0 开始；`index=1` 指第二个输入片段。适配器读取 `results[].index` 与 `relevance_score`，忽略供应商附带的正文。Jina 的 `/v1/rerank` 和 Cohere 的 `/v2/rerank` 提供这种核心契约；其他供应商要核对请求字段和返回结构后再接入。[Jina 官方 API](https://api.jina.ai/docs)、[Cohere Rerank API](https://docs.cohere.com/reference/rerank)。

我们令 `top_n` 等于输入候选数，要求每个输入位置恰好出现一次：数量不全、重复索引、越界索引、缺失字段、NaN/Inf 分数都会报错。零分是有效分数，用指针区分「零」和「未返回」。分数相同时按重排前名次稳定排序。

Gateway 和 Service 都会校验索引，测试注入的 fake 实现也不能凭空添加来源。最终引用编号由已选中的片段重新分配，重排前的 `[1]` 不会被误当成重排后的 `[1]`。

## 3. 三种分数分别代表什么

| 字段 | 来源 | 用途 |
| --- | --- | --- |
| `similarity` | 问题向量与片段向量 | 保留向量召回的证据门槛 |
| `fusionScore` | RRF 的名次融合 | 保存混合检索的初始顺序 |
| `rerankScore` | 重排模型 | 决定重排后的顺序 |

重排分数不与其他分数直接相加，也不作为答案正确概率。不同模型的分数尺度可能不同，本节只要求它是有限数，不强制限制在 0～1。

`preRerankRank` 保存模型调用前的候选位置，`rerankRank` 保存调用后的名次。原始来源 ID、正文、余弦分数、关键词名次和 RRF 分数均保留。历史消息仍保存原有引用快照；重排诊断保存在日志和离线评估快照中。

## 4. 请求前预算与失败行为

Gateway 检查候选数量、单个「问题 + 片段」的估算 Token，以及整批输入的估算 Token。总预算采用每对都计一次问题的保守估算；不同模型的真实计费方式可能不同，最终用量以供应商报告为准。

超限在 HTTP 请求前报错，不静默截断正文或少传候选。默认每路召回 30 个，混合检索并集最多 60 个，与默认重排数量上限一致。扩大 `RAG_RETRIEVAL_CANDIDATE_LIMIT` 时，应同时核对重排模型的数量与 Token 限制。

连接超时由 HTTP Transport 控制，整次调用通过 `context.WithTimeout` 控制。用户取消的 Context 会一直传到 HTTP 请求。本节每次最多发起一次重排请求；上游失败、超时或响应无效时，检索返回错误。需要回到基线时使用服务端配置开关。

相似度门槛继续沿用原有规则：纯向量候选仍需通过余弦阈值；混合检索中有真实关键词命中的候选可走关键词路径。即使重排给了高分，也不会跳过这些规则。最终 Top-K 和上下文 Token 预算在重排之后执行。

观测包含 Provider、请求模型/返回模型、候选数、估算 Token、实际 Token（如果提供）、实际 search units（如果提供）及耗时。缺失用量保持未知，不填 0 或用估算冒充；这些用量独立记录，不混入 Chat 模型的 Token。当前没有按供应商价格推算美元成本。

## 5. 怎样配置和试用

`.env` 与 `.env.example` 已同步，默认 `RERANK_ENABLED=false`。开启时填写：

```dotenv
RERANK_ENABLED=true
RERANK_PROVIDER=自己的观测标识
RERANK_ENDPOINT=https://供应商的完整重排接口地址
RERANK_MODEL=已开通的重排模型名
RERANK_API_KEY=实际密钥
RERANK_TOKENIZER=heuristic
RERANK_MAX_DOCUMENTS=60
RERANK_MAX_PAIR_TOKENS=4096
RERANK_MAX_TOTAL_TOKENS=32768
RERANK_CONNECT_TIMEOUT=5s
RERANK_REQUEST_TIMEOUT=15s
```

预算和 Tokenizer 应按部署模型调整，默认值不是供应商能力声明。远程服务通常需要 Key，本地无鉴权兼容服务可留空。重启后端生效。它是部署配置，网页无需增加模型或开关字段。

## 6. 怎样做对比实验

固定问题集、源文档、Embedding、召回方式与候选上限，再分别抓取：

```bash
cd backend
RERANK_ENABLED=false go run ./cmd/rageval capture \
  -dataset /path/to/private-dataset.json -user-id <用户UUID> \
  -name baseline -out /path/to/baseline.json

RERANK_ENABLED=true go run ./cmd/rageval capture \
  -dataset /path/to/private-dataset.json -user-id <用户UUID> \
  -name reranked -out /path/to/reranked.json

go run ./cmd/rageval evaluate -dataset /path/to/private-dataset.json \
  -runs /path/to/baseline.json,/path/to/reranked.json
```

快照保存重排模型、Tokenizer、准入限制、每个片段的前后名次与分数、耗时和可选用量。离线先重建原始向量/RRF 顺序，再复用 `ApplyKnowledgeRerank` 按保存的分数排序，并校验记录名次是否一致。修改 Top-K 或阈值时可以复用同一问题和完整候选池的分数；重新生成答案及人工复核仍需另做。

重点比较：Candidate Recall 是否相同、MRR/nDCG 是否提高、最终 Recall@K 是否改善，以及新增重排延迟和用量是否值得。回答质量要重新生成后判断，检索分数提升不能直接等同于回答更准确。

仓库新增 `demo-rerank.json`，使用与向量基线相同的候选，演示重排改善排序，同时保留「开票文档没有召回」的失败。它完全是合成分数，没有真实供应商效果结论，也没有填写生成质量标签。

```bash
go run ./cmd/rageval evaluate -dataset eval/rag/demo-dataset.json \
  -runs eval/rag/demo-baseline.json,eval/rag/demo-rerank.json
```

## 7. 验证和学习顺序

测试覆盖兼容协议、必需字段、HTTP 错误、完整索引排列、同分稳定性、请求前数量/Token 预算、取消、超时、权限先行、重排关闭不调用、引用映射，以及向量/混合两种快照的离线重放。HTTP 测试使用本地 `httptest` 服务，不消耗真实模型额度。

建议先读 `knowledge_rerank_test.go` 的两条候选示例，再读 `ApplyKnowledgeRerank`，最后读 Gateway 的预算与校验。下一节学习查询改写：它发生在召回之前，与本节只处理已有候选的阶段不同。
