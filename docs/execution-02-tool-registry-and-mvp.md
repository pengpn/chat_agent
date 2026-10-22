# 执行工具第二课：统一 Registry 与 12 个 MVP 工具

本课暂不继续 RAG。目标是把已有的文件/Shell 能力接入一个完整但有边界的 Agent 工作循环：计划 → 收集信息 → 获取授权 → 执行 → 保存产物。

代码结构随后按[第三课：类型化工具与业务服务](./execution-03-typed-tools-and-services.md)重构，功能、工具名、网页接口和数据表保持兼容；本文的代码定位已同步更新。

## 1. 本次完成了什么

| 逻辑名称 | 供应商名称（WireName） | 作用与边界 |
| --- | --- | --- |
| `shell.exec` | `shell_exec` | Docker 沙箱执行，需要当前用户对完整参数进行一次性审批 |
| `file.list` | `file_list` | 分页浏览当前用户、当前会话的工作区 |
| `file.read` | `file_read` | UTF-8 分段读取并返回文件 SHA-256 |
| `file.write` | `file_write` | 创建/覆盖；覆盖已有文件必须校验版本 |
| `file.apply_patch` | `file_apply_patch` | 精确文本替换，不是 unified diff |
| `file.search` | `file_search` | 字面量或 RE2 正则内容搜索 |
| `web.search` | `web_search` | Tavily 网页摘要检索，默认关闭，不允许模型指定任意上游地址 |
| `plan.update` | `plan_update` | 保存完整计划，最多 20 步、最多一个进行中步骤 |
| `user.ask` | `user_ask` | 创建真实用户问题，等待浏览器提交答复 |
| `user.request_approval` | `user_request_approval` | 请求对敏感工具的一次性批准；它自己不执行目标工具 |
| `artifact.save` | `artifact_save` | 将文本/Markdown/JSON 产物持久化为不可变记录 |
| `artifact.read` | `artifact_read` | 按产物 ID 读取当前会话内容，支持字节分页 |

六个执行工具沿用第一课的隔离网关，不把宿主机文件操作和 `os/exec` 直接开放给模型。原来的 `calculator` 仍保留，因此部署启用执行和搜索后共 13 个工具，其中 12 个是本课清单。

## 2. 为什么只有一个 Registry

相关代码：`backend/internal/tool/tool.go`、`registry.go`。

Registry 是统一的注册、Schema 校验、权限、审批和审计入口，而不是按分类创建多个互相独立的白名单。

```go
type Metadata struct {
    Namespace        string   // 例如 file、shell、user
    Name             string   // 例如 read、exec、request_approval
    Version          string   // 服务端显式选择的语义版本，例如 1.0.0
    Category         string   // 用于产品展示/管理的业务分类
    Source           string   // 当前为 builtin，未来可扩展其他来源
    Exposure         string   // 当前为 model，说明暴露方式
    Capabilities     []string // 能力说明，不代替权限判断
    WireName         string   // 供应商协议中实际使用的下划线名称
    RequiresApproval bool     // 是否必须消费服务端授权票据
}
```

例如，`builtin:file.read@1.0.0` 是带来源和版本的工具身份；模型实际调用 `file_read`。业务分类不是安全策略：不能因为 Category 是 file，就默认可以读任意目录。

`Register` 会编译 JSON Schema，拒绝重复的“逻辑 ID + 版本”、不同工具占用同一 WireName，以及同一工具不同版本改变 WireName。新增版本不会自动覆盖当前版本，必须由服务端显式 `Activate`。固定内置工具在构造期注册失败属于程序错误，因此构造函数会 panic，避免带着错误定义启动。

现有的简单 `Tool` 接口保持兼容；未实现 Metadata 的旧适配器会映射到 `builtin:legacy.*@1.0.0`。正式内置工具已全部提供显式元数据，后续新增工具也应这样做。

## 3. 为什么要固定本轮 ToolSet

相关代码：`ChatSession.toolSet` 与 `Registry.SnapshotForUser`。

1. Prepare 普通消息、重新生成、编辑消息时，按用户权限创建 ToolSet。
2. 从 ToolSet 取出定义，随请求发送给模型。
3. 后续所有工具轮次都用同一个 ToolSet 执行。
4. 执行前再次检查用户权限和参数 Schema。

这保证“模型看到的 Schema”和“真正运行的执行器”是同一个版本。即使之后注册或激活新版本，正在生成的消息不会中途切换。模型也不能调用本轮没有暴露的工具。

审计日志包含工具逻辑 ID、版本、来源、Schema SHA-256、调用 ID、用户/会话和参数摘要。参数原文不写入日志。`Configure`、`SetApprovalGuard` 用于应用启动期装配，不是运行期热修改接口。

## 4. 提问不是返回一段“请用户回答”的字符串

相关代码：`tool/interaction.go`、`service/interaction.go` 的 `Ask`、`wait`、`Respond`。

`user.ask` 真正执行以下流程：

```text
模型调用 user_ask
  → 数据库创建 pending 问题
  → 发布 agent.state.changed 用户 SSE 事件
  → 浏览器重新读取权威状态，展示答复卡片
  → 用户点击“提交答复”
  → HTTP 鉴权 + 会话所有权 + 状态/有效期校验
  → 数据库 pending → responded
  → 等待中的工具读取真实答复，作为 role=tool 结果回传模型
```

模型没有“代替用户答复”的工具接口。问题支持建议选项，但用户仍可输入自己的内容。问题不能提交批准字段；审批不能用文本“approved”冒充批准。

本版通过每 300ms 查询数据库等待用户响应，便于理解且可跨请求读取状态；不是持久化工作流引擎。浏览器以 SSE 为主通知，生成期间每 2s 轮询兜底。切换会话不会主动取消原有生成，切回会话后能读取仍有效的问题。

## 5. 一次性审批如何防止被绕过

### 5.1 模型先申请，而不是先执行

```json
{
  "tool": "shell_exec",
  "arguments": { "command": "printf 'hello\\n'" },
  "reason": "验证沙箱中的基础命令执行"
}
```

这是 `user_request_approval` 的参数。服务端只允许为本轮可见、确实标记 RequiresApproval 的工具申请。网页会显示原因、逻辑名、版本和完整参数，不默认批准。

用户选择“仅允许这一次”后，模型还需要实际调用：

```json
{ "command": "printf 'hello\\n'" }
```

### 5.2 票据绑定哪些信息

数据库按 userID、conversationID、工具 OperationID 隔离。工具任务身份是“LLM 业务 OperationID + 已落库的 assistant 消息 ID”的 SHA-256：没有幂等头也有唯一身份，进程重启后复用客户端幂等键也不能复用旧消息审批，且数据库索引不存无限长度的客户端原始键。它不会改变 LLMGateway 原本的幂等语义。再把下列内容组成 ApprovalKey：

- 工具 Metadata：来源、逻辑名称、版本等；
- 固定的 SchemaHash；
- 完整参数的规范 JSON 编码。

对象字段顺序和空格变化不影响匹配，但改动命令、增加字段、切换版本或换到另一个任务都不会获得授权。参数规范化保留 JSON 数字文本，不做可能损失精度的 float64 转换。

`ConsumeApproval` 在 PostgreSQL 事务中锁定票据，把 `responded` 改为 `consumed`，并发请求也只有一个能成功。拒绝、过期、已取消、已消费和普通问题都不能授权。缺少审批服务时 Registry 默认拒绝，不降级为直接执行。

审批不等于提权：角色策略、执行网关和 Docker 沙箱仍然生效。工作区内的 `file.write`/`file.apply_patch` 本版不逐次审批，因为它们已限制在专属工作区且有 SHA-256 比较；Shell 可能批量更改工作区，因此统一要求审批。

### 5.3 取消与超时

工具继承业务 Context。停止生成、删除会话、退出登录、关闭页面引发原请求取消时，等待立刻结束，并通过独立的短超时 Context 将未完成交互改为 canceled。

`TOOL_HUMAN_TIMEOUT` 默认 90 秒，用户交互工具可用该专属时长，而非普通工具的 30 秒；仍受 `STREAM_TOTAL_TIMEOUT` 剩余时间限制，不能延长整个业务请求。

重启进程不会自动恢复已中断的模型调用；残留 pending 最晚到 ExpiresAt 后不再展示/接受响应。已有审批也只能在原 OperationID 内使用，不能拿去授权新任务。

## 6. 计划与产物的数据设计

新增三张表：

- `agent_plans`：每个会话一份当前计划，更新时 revision 原子递增。
- `tool_interactions`：问题/审批、请求内容、用户响应、状态、任务 ID、审批摘要及有效期。
- `agent_artifacts`：产物 ID、用户/会话、标题、媒体类型、文本、SHA-256、大小。

Model 位于 `internal/database/model/agent_tool.go`；全部 GORM 操作位于 `repository/agent_tool.go`，接口和实现放在同一个文件。`PlanService`、`InteractionService`、`ArtifactService` 分别编排业务，`ToolStateService` 只读聚合网页状态；Handler 解析请求和返回响应。没有把数据库操作挤进工具适配器或 server.go。

产物不是工作区文件：它是用户可以长期查看/下载的交付物。`artifact.save` 每次创建新 ID，最多 32 KiB，JSON 类型需要语法有效；`artifact.read` 最多读 4096 字节，并修正 UTF-8 边界，下一页使用 nextOffset。前端预览转义文本、不执行 HTML，下载按纯文本处理。

三张表都由 Conversation 外键级联删除；账户删除通过会话级联清理。注意：第一课的磁盘工作区生命周期回收仍是后续 TODO，不要将数据库级联清理误认为宿主工作区也已经回收。

当前计划和产物按会话展示，不按消息分支划分；产物列表展示最近 50 项。更完整的历史分页、关联消息和配额控制可在后续增加。

## 7. 联网搜索为什么单独封装

相关代码：`internal/websearch/tavily.go`。

`web.search` 不是“让模型自己上网”，而是服务端调用固定搜索 API，返回有限的标题、URL 和片段。实现依据 [Tavily 官方 Search API 文档](https://docs.tavily.com/documentation/api-reference/endpoint/search)。

- 固定 HTTPS 地址和 Bearer 认证；模型不能替换上游 URL。
- basic 搜索、不请求原始网页内容/图片/额外答案，不自动跟随重定向。
- 请求带 Context、HTTP 超时、响应体大小和结果条数限制。
- 过滤非 HTTP(S) 或带账号密码的结果链接，限制片段和标题长度。
- 不记录 Key、查询原文或上游错误响应体。
- 结果带 `untrusted=true`，只作为资料引用，不把网页中的指令当作系统授权。

搜索服务独立计费，尚未接入 LLM Token 用量表；本次没有使用真实 Key 进行付费搜索。对网页内容的提示注入风险仍需要依赖服务端权限、审批和沙箱共同约束，单靠一句“不可信”提示不是安全保证。

## 8. 配置与动手练习

已同步 `.env`、`.env.example` 并提供中文注释：

```dotenv
MAX_TOOL_ITERATIONS=8
TOOL_HUMAN_TIMEOUT=90s
WEB_SEARCH_ENABLED=false
WEB_SEARCH_API_KEY=
WEB_SEARCH_TIMEOUT=10s
WEB_SEARCH_MAX_RESULTS=5
```

计划、提问、审批和文本产物不依赖外部搜索或 Docker，默认可用。文件和 Shell 仍需按第一课构建镜像并启用 EXECUTION_ENABLED，默认只向管理员开放。Shell 现在还必须经过网页审批。

练习建议：

1. “先用工具创建两步计划，再询问我目标读者，等我回答后保存一份 Markdown 报告产物。”观察 tool_calls 与 role=tool 的循环，以及网页卡片。
2. 开启执行工具后：“在工作区创建 hello.txt，申请运行 cat hello.txt，等我批准后再执行，并把结果保存为产物。”
3. 点击拒绝，确认没有 Docker 执行；重新申请批准后，修改命令再次执行，确认旧票据不能复用。
4. 开启联网搜索后：“使用联网搜索查找 Go 官方错误处理文档，回答中标注来源 URL。”不要把返回片段视作新的执行指令。

是否调用工具最终由模型的 tool_choice:auto 决策。教学时可以明确要求用指定工具；没有配置或没有权限的工具不会出现在本轮定义中。

## 9. 如何验证

```bash
cd backend
go test ./...
go vet ./...
go test -race ./internal/tool ./internal/service ./internal/websearch

# 使用独立测试数据库或本地开发库；测试创建带专用前缀的数据并清理。
TEST_DATABASE_URL='你的测试数据库连接串' go test ./internal/repository \
  -run TestPostgresAgentToolsPersistenceApprovalAndCascade -count=1

cd ../frontend
npm test
npm run build
```

新增测试覆盖注册冲突、版本固定、Schema 防伪造参数、越权/参数变化/跨任务/重复审批、取消等待、UTF-8 产物分页、计划修订、Tavily 请求/脱敏/重定向限制，以及真实 PostgreSQL 的并发一次性消费和级联删除。

前端继续遵循现有设计系统的暖白、石墨灰、深青色和语义 Token。新增“工具工作卡”规范已写入 COMPONENTS.md；不引入新的视觉风格或浏览器 alert。

本次验证记录：后端完整测试、vet 与工具/业务/搜索竞态测试通过；本地 PostgreSQL 实测迁移、8 路并发单次消费和级联清理通过。浏览器用模拟数据验证了 375、760、1024、1440px 下的独立工作卡与完整 ChatPanel，检查无横向溢出、消息区可滚动、44px 点击区域、减少动态效果、真实按钮提交和不可信文本转义。没有调用真实 LLM 或付费搜索接口。

## 10. 这还不是什么

- 不是可跨进程恢复的 Durable Workflow；等待中重启仍需重新生成。
- 不是任意网络访问工具；Shell 仍禁网，web.search 只访问固定搜索服务。
- 不是长进程交互终端：`shell.write_stdin`、`file.diff` 仍留在后续阶段。
- 不是完整生产多租户执行平台：工作区清理、独立执行节点、累计产物/交互存储配额及搜索计费仍需后续加强。
- 审批票据提供一次性执行门禁，不等于所有外部系统副作用都具有端到端 exactly-once 语义。
