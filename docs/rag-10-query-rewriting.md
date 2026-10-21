# RAG 第十阶段：查询改写与多轮检索评估

这一节补上多轮对话中的一个常见缺口：用户的问题依赖前文，但向量检索只看到了当前这一句话。

例如：

```text
用户：介绍一下专业版订阅。
助手：专业版提供按月订阅……
用户：它的退款期限是什么？
```

如果直接给 `EmbedQuery` 输入「它的退款期限是什么」，检索模型不知道「它」指专业版。查询改写先把问题补全成「专业版订阅的退款期限是什么」，再执行检索。

本节只实现一次独立问题改写，不实现多查询扩展、HyDE 或自动搜索循环。改写不保证提高质量，所以默认关闭；真正开启前，需要自己的多轮问题集做对比。

## 1. 先区分三个模型任务

| 任务 | 输入 | 输出 | 本项目入口 |
| --- | --- | --- | --- |
| 查询改写 | 原问题 + 有界近期历史 | 一条可独立检索的问题 | `QueryRewriter` → `LLMGateway.Chat` |
| 查询 Embedding | 实际检索问题 | 一个向量 | `EmbeddingGateway.EmbedQuery` |
| 回答生成 | 原问题 + 对话上下文 + 检索证据 | 面向用户的答案和引用 | `ChatService` → `LLMGateway` |

查询改写使用生成模型，不是向量模型；它的输出是文字，不是向量。上一节的 Reranker 则是在已有候选中调整顺序，不能补召回；改写发生在召回之前，可能改变整个候选池。

## 2. 当前完整链路

```text
原始问题 ────────────────────────────────────┐
   │                                         │
   ▼                                         │
校验参数和知识库所有权                         │
   │                                         │
   ▼                                         │
QueryRewriter + 当前分支的近期完整正文          │
   │ 成功：独立问题；失败：原问题               │
   ▼                                         │
Embedding → 向量召回 / 关键词召回 + RRF        │
   ▼                                         │
可选 Reranker → 证据门槛 → Top-K / Token预算    │
   │                                         │
   └────────── 检索证据 ──────► 回答生成 ◄─────┘
```

重点：改写问题只给向量检索、关键词检索和重排使用。数据库里的用户消息、前端展示，以及回答生成时的当前问题，都保留原文。引用仍来自实际选中的知识库片段，不来自改写模型的输出。

没有选择知识库的普通聊天不会触发改写。公开检索接口也可使用改写，但没有对话历史；HTTP JSON 不能自行注入 `history`。

## 3. 建议按这个顺序读代码

所有代码路径均相对于项目的 `backend/`：

1. `internal/service/query_rewrite_test.go`：先看「它」如何变成明确的产品问题，以及失败如何回退。
2. `internal/service/query_rewrite.go`：历史裁剪、输入预算、结构化调用、输出防线和观测。
3. `internal/service/chat_grounding.go` 与 `chat.go`：历史从哪里来，为什么原问题不被替换。
4. `internal/service/knowledge_retrieval.go`：权限校验后插入改写，并向下游传递实际检索问题。
5. `internal/config/query_rewrite.go`：服务端模型配置和边界检查。
6. `internal/rageval/query_rewrite.go` 与 `cmd/rageval/`：快照校验与开关对比。

### 3.1 为什么历史不能直接从最终 ChatRequest 获取

当前项目先做检索，再执行 `prepareContext` 组装最终对话上下文。在检索时，临时 `ChatRequest` 主要包含系统提示和当前用户消息，尚没有完整历史。

所以这次明确传入服务端已经读取的消息：

- 新消息使用 `selectedBranch(history)`，只取当前选中的分支。
- 重新生成使用原用户消息之前的 `ancestryBefore`，不把旧回答和之后的问题作为依据。
- 编辑消息使用被编辑消息之前的 `ancestryBefore`，不把被替换的原问题或兄弟分支带进去。

`queryRewriteHistory` 只保留 `user/assistant` 且 `status=complete` 的正文。不包含系统提示、隐藏思考、工具结果、附件正文或图片；也不使用完整会话摘要。这是本节有意设置的范围，指代超出近期历史时应保守保留原问题。

`KnowledgeRetrievalRequest.History` 标记为 `json:"-"`。网页提供会话和问题，历史由服务端根据所有权与分支读取；只有受控的离线评估集可以显式提供历史。

### 3.2 历史有两道预算

第一道限制消息条数和 Unicode 字符总量：从最近向前取完整消息，超出时停止，不截断半个实体。遇到过长的最近消息时，不会跳过它再拿更早的语境来猜指代。

第二道调用 `LLMGateway.EstimateInputTokens`，估算完整请求，包括系统提示、结构化输出约束、JSON 包装、当前问题和保留历史。如果超出输入预算，继续丢最旧的完整消息；只剩当前问题仍超限，就不调用改写模型，直接回退原问题。

`MaxOutputTokens` 另行限制改写输出。之后 `LLMGateway` 仍会执行自身的模型窗口校验、总预算检查、Redis Token 桶准入和内部重试；业务服务不直接调用供应商 SDK。

## 4. 为什么要结构化输出

服务端固定一个严格 JSON Schema，要求模型返回：

```json
{
  "query": "专业版订阅的退款期限是什么？",
  "useOriginal": false
}
```

如果原问题已经明确，或者历史有多个可能指代，模型应返回 `useOriginal=true`。服务端此时直接使用自己持有的原问题，不信任返回的 `query`。

Schema 校验由现有 `LLMGateway` 统一处理。改写调用关闭主动思考，不提供工具；历史和问题通过一条用户角色 JSON 数据消息传入，不能变成系统角色指令。提示词明确要求忽略数据里的命令、不能回答问题、不能添加外部事实。

拿到结构化结果后，服务端还检查：非空、改写后的字符上限、数字/英文型号/编号字面量，以及一组常见中文否定和范围词。比如 `SKU-42` 不能变成 `SKU-43` 或 `SKU-420`，「不能」不能被删成肯定句。

这只是保守防线，不是语义等价证明。它不能可靠识别所有中文实体、中文数词、复杂否定或历史里本身错误的指代。JSON 合法也不代表意思正确，因此后面必须人工评估“指代是否正确、条件是否保留”。

## 5. 超时、错误和取消的区别

| 情况 | 行为 | 观测状态 |
| --- | --- | --- |
| 开关关闭 | 不调用改写模型，直接用原问题 | `disabled` |
| 模型选择保留原文 | 继续用原问题 | `unchanged` |
| 改写合法且发生变化 | 用改写问题检索 | `rewritten` |
| 输入预算超限、供应商错误、限流、JSON 无效、条件防线失败 | 回退原问题检索 | `fallback`，记录原因 |
| 改写专用超时 | 回退原问题检索 | `fallback/timeout` |
| 上游 Context 取消或到期 | 返回上游错误，终止后续 Embedding/检索 | `canceled/parent_context` |

改写超时使用父 Context 派生的 `context.WithTimeout`。专用的 5 秒期限到达，并不意味着整个聊天已被取消；但用户点停止等操作导致父 Context 取消时，不能通过“降级”为借口继续消耗模型额度。

这层不自行加重试循环，重试仍由 `LLMGateway` 管理。一次改写的内部 attempt 使用同一个 OperationID，便于追踪；它不取代对话入口包住整条业务消息的幂等机制。

## 6. 配置与试用

`.env`、`.env.example` 已同步以下中文注释配置，默认关闭：

```dotenv
RAG_QUERY_REWRITE_ENABLED=false
RAG_QUERY_REWRITE_PROVIDER=
RAG_QUERY_REWRITE_MODEL=
RAG_QUERY_REWRITE_MAX_HISTORY_MESSAGES=6
RAG_QUERY_REWRITE_MAX_HISTORY_CHARS=6000
RAG_QUERY_REWRITE_MAX_INPUT_TOKENS=2048
RAG_QUERY_REWRITE_MAX_OUTPUT_TOKENS=256
RAG_QUERY_REWRITE_TIMEOUT=5s
```

启用时，将开关改为 `true`，Provider/Model 填成项目已经配置密钥、并出现在该 Provider 模型列表中的组合。可以选择与回答不同的较小模型；这里不额外定义一套 API Key/Endpoint。启动时会验证目标存在，单次调用仍经过正式网关。

重启后端后，在同一对话中选择知识库，先明确产品，再问「它的退款期限是什么」。网页不需要新增开关、提示词或 `responseFormat` 参数。

改写多一次生成调用，增加延迟和费用。不要仅凭模型更小就推断总成本更低：调用次数、输入历史长度、输出和重试也会影响成本。

## 7. 日志和用量怎么看

普通 `knowledge query rewrite completed` 结构化日志保存：状态、失败原因、Provider/Model、提示词版本、实际输入数据哈希、使用的历史条数、估算输入 Token、耗时，以及供应商上报的输入/输出 Token。

价格已在 Provider 定价中配置且供应商返回用量时，才计算 `costNanoUsd`，单位为十亿分之一美元。`usageReported=false` 或 `costKnown=false` 表示未知，不能把数值字段的零当成“没消耗/免费”。错误或超时也可能已经产生费用；没有报告的消耗无法精确推算。

这些用量独立记录，不混进回答模型的消息 Token。当前这一节没有将改写费用另存为消息计费明细或接入用户日/月配额账本；统一网关的请求预算和 Token 桶仍然生效。

普通日志不记录原问题和历史正文。原问题、实际检索问题及候选原文只在显式开启的 `RAG_RETRIEVAL_TRACE_ENABLED=true` 诊断日志，或 `rageval capture` 输出中保存。这些内容有隐私风险，应保存在受控位置，不能把真实用户资料提交到公共仓库。哈希也不是匿名化保证。

## 8. 如何对比开关前后

新增 `eval/rag/demo-query-rewrite-dataset.json` 展示三类问题：可补全指代、需要保留型号/年份/否定条件、历史有两个对象导致指代不明。它是合成格式样例，不是已跑出的真实模型成绩。

正式实验需把文档 ID、知识库 ID、标准答案和历史换成真实、已标注的数据。第一版只改查询改写开关，固定同一份历史、原问题、源文档、索引、Embedding、召回模式、重排配置、Top-K 和阈值。

```bash
cd backend
# private/ 已加入 .gitignore；仍要自行限制本地访问权限。
mkdir -p eval/rag/private

# 先准备 eval/rag/private/dataset.json，并在 .env 配好改写模型。
RAG_QUERY_REWRITE_ENABLED=false go run ./cmd/rageval capture \
  -dataset eval/rag/private/dataset.json -user-id <用户UUID> \
  -name original-query -out eval/rag/private/original.json

RAG_QUERY_REWRITE_ENABLED=true go run ./cmd/rageval capture \
  -dataset eval/rag/private/dataset.json -user-id <用户UUID> \
  -name rewritten-query -out eval/rag/private/rewritten.json

go run ./cmd/rageval evaluate -dataset eval/rag/private/dataset.json \
  -runs eval/rag/private/original.json,eval/rag/private/rewritten.json
```

`capture` 会调用真实模型并可能产生费用；当 LLM 限流开启时，CLI 的改写请求也使用正式 Redis Token 桶。`evaluate` 和 `sweep` 只读快照，不调用模型。

快照新增：实际检索问题、历史指纹、改写观测和完整配置。历史指纹用于拒绝“问题相同但偷偷换历史”的不公平比较。旧的无历史快照仍兼容。模型、提示词版本、状态、预算与用量标记不一致时会拒绝评估。

报告新增 `meanRewriteMs`、`rewrittenCount`、`rewriteFallbackCount`、改写输入/输出 Token，以及已知成本与已报告用量的案例数量。已知成本之和不是完整账单；还有未知案例时不能据此认定总费用。

同时继续观察 Candidate Recall、Recall@K、MRR、nDCG、无证据误选和人工标注的回答/引用质量。额外人工核对改写文本是否补错对象、修改了日期或否定条件。当前报告不会自动给出语义等价分数，也不会自动生成并评判最终答案。

改写会改变查询向量和候选池，不能对旧快照点一下 `sweep` 就得到改写实验；必须重新 `capture`。只有同一批已捕获候选的 Top-K/阈值调整才适合重放。

## 9. 本节的验收与练习

测试覆盖：默认关闭、配置校验、权限先于模型、结构化网关、历史条数/字符/Token 预算、字面量防线、超时回退、上游取消后不再 Embedding、三种对话入口的分支隔离、HTTP 不接受伪造历史，以及快照历史/模型/状态一致性。

建议先调试 `Rewrite` 的几个断点：历史裁剪结束、`EstimateInputTokens` 返回、`gateway.Chat` 返回、最后输出校验。再回到 `knowledge_retrieval.go` 看同一条 `query` 如何依次进入 Embedding、关键词和 Reranker。

下一步先用 10～20 个自己的多轮问题做小规模标注：明确问题、单一指代、跨多轮省略、多个候选对象、编号/日期/否定条件、无答案。保留发生回退和质量变差的案例；用实际收益决定是否开启，不急于叠加更多模型调用。
