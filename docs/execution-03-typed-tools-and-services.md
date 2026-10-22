# 执行工具第三课：统一工具适配器与类型化业务服务

本课只重构实现方式，不增加工具、不改变网页接口或数据表。解决的问题是：上一版文件工具在适配器里分派，新工具却把原始 JSON 和工具名一起交给 `AgentToolService`，两种写法不统一，业务服务也承担了过多职责。

如果还不清楚 Tool 为什么存在、如何加载以及一次对话怎样进入工具执行，建议先阅读 [Tool 学习指南](./Tool学习指南.md)，其中包含完整新增工具练习。

## 1. 重构前的问题

```text
executionTool.Execute(JSON)
  → 再校验一次 Schema
  → switch 工具名
  → execution.Gateway

mvpTool.Execute(JSON)
  → AgentToolService.ExecuteTool(工具名, JSON)
  → switch 工具名、解析 JSON
  → 搜索 / 计划 / 审批 / 产物
```

分层本身没有错：文件和 Shell 应交给隔离执行网关，数据库业务应交给 Service。需要修正的是：工具分派、传输参数解析的位置不一致，并且用一个业务服务包办多个领域。

## 2. 现在的唯一执行结构

```text
ChatService
  → 本轮 ToolSet / 统一 Registry
      白名单、权限、Schema、审批门禁、超时、结果大小、审计
  → 具体 Tool.Execute
      严格解码 JSON、调用类型明确的方法、编码 JSON 结果
  → 对应 Service / Gateway
      业务规则、所有权、状态流转或基础设施隔离
  → Repository / Docker / 搜索 API
```

每一个具体工具都实现相同的 `Tool` 接口，没有新增第二套 AgentTool 接口或第二个 Registry。

| 工具文件 | 具体适配器 | 类型明确的依赖 |
| --- | --- | --- |
| `tool/file.go` | FileList/Read/Write/ApplyPatch/SearchTool | FileOperations，由 execution.Gateway 实现 |
| `tool/shell.go` | ShellExecTool | ShellRunner，由 execution.Gateway 实现 |
| `tool/web.go` | WebSearchTool | websearch.Searcher |
| `tool/plan.go` | PlanUpdateTool | PlanUpdater，由 PlanService 实现 |
| `tool/interaction.go` | UserAskTool、UserRequestApprovalTool | UserInteractor，由 InteractionService 实现 |
| `tool/artifact.go` | ArtifactSaveTool、ArtifactReadTool | ArtifactStore，由 ArtifactService 实现 |

原来的 `tool/execution.go`、`tool/mvp.go` 和 `service/agent_tool.go` 已移除。计算器也复用相同的解码与结果编码入口，具体计算仍调用自己的纯计算函数。

## 3. 为什么 Tool 仍有 Execute(JSON)，Service 却不再接收 JSON

模型协议规定工具参数通过 JSON 传输，所以统一 Tool 接口保留：

```go
Execute(ctx context.Context, arguments string) (string, error)
```

但传输格式不需要继续扩散到业务方法。计划工具现在调用：

```go
type PlanUpdater interface {
    Update(context.Context, domain.UpdatePlanInput) (domain.Plan, error)
}

func (t *PlanUpdateTool) Execute(ctx context.Context, args string) (string, error) {
    return executeTyped(ctx, args, t.plans.Update)
}
```

PlanService 的入口则是：

```go
Update(ctx context.Context, input domain.UpdatePlanInput) (domain.Plan, error)
```

它只关心计划步骤、状态、用户身份和保存规则，不再知道模型调用时的名字是 `plan_update`，也不需要通过 switch 判断自己应该做什么。

Go 使用隐式接口实现：PlanService 拥有匹配的 Update 方法，就可以注入 PlanUpdater，不需要在业务服务中声明“实现了工具接口”。

## 4. executeTyped 与 toolSpec 各负责什么

`tool/adapter.go` 提供两个小的复用构件。

### executeTyped：复用边界代码

```go
func executeTyped[Input, Output any](
    ctx context.Context,
    raw string,
    execute func(context.Context, Input) (Output, error),
) (string, error)
```

它依次检查 Context、要求 JSON 对象、严格解码、禁止未知字段和尾随第二个对象、调用类型明确的方法，最后编码结果。Go 可以根据传入的方法自动推导 Input/Output 类型。

它不是执行分派器：没有工具名参数、没有工具名 switch，也不决定权限或选择实现。具体依赖在构造对应 Tool 时就已经固定。

### toolSpec：复用固定描述

只保存 Definition、Metadata 和可选执行超时。具体 Tool 嵌入它后复用这些方法，但仍自己实现 Execute。它不持有 Repository，也不启动 Docker。

`builtin.go` 的 NewBuiltinTools 只做依赖装配。哪项依赖未配置，就不装配对应能力；注册校验和版本选择仍交给同一个 Registry。原先按“MVP 阶段”命名的装配方法不再保留。

## 5. 校验统一，并不等于删除业务保护

现在 Schema 只在 Registry 注册时编译，在每次正式执行前校验；文件工具不再重复编译同一份 Schema。

不同层仍保留不同含义的检查：

- Registry：参数是否符合模型公开契约，例如必填字段、枚举、额外字段和大小。
- 适配器：JSON 是否能完整、无损地转换成 Go 参数，不静默忽略未知字段。
- Service：业务规则，例如一个计划最多一个进行中步骤、产物 JSON 内容是否有效、会话是否属于当前用户。
- execution.Gateway：相对路径、工作区隔离、容量、SHA-256 版本条件与 Docker 安全限制。

不能因为 Schema 已检查字符串，就删除网关的路径防护；也不能因为适配器调用正常，就允许一个计划出现两个进行中步骤。

生产调用必须经过 Registry/ToolSet；直接调用具体 Tool.Execute 不是绕过权限和审批的受支持入口。

## 6. 业务服务如何拆分

- `PlanService.Update`：计划业务校验、保存与修订版本。
- `ArtifactService.Save/Read/Get`：不可变产物、内容校验、UTF-8 分页与网页读取。
- `InteractionService.Ask/RequestApproval/Respond`：真实用户交互、等待、超时与取消。
- `InteractionService.Authorize`：为 Registry 提供一次性审批消费门禁。
- `ToolStateService.Get`：只读聚合计划、待处理交互与产物，供网页显示。

`toolAccess` 只复用会话所有权检查与 SSE 通知，没有通用 ExecuteTool，也不把拆掉的大服务重新包回来。

Service 仍会使用 JSON：计划步骤保存在 JSONB 中，JSON 产物需要语法检查，审批需要对不透明参数生成指纹。这些是持久化/业务需求，不是继续接收“工具名 + 原始模型参数”进行分派。

## 7. 审批为什么仍然安全

`UserRequestApprovalTool` 先解析模型传来的目标名称，再调用 ResolveApproval，从本轮固定 ToolSet 中找到真实的目标 Descriptor，并校验目标参数。

随后传给业务服务的是：

```go
domain.ApprovalRequest{
    Target:    target, // 服务端解析的固定身份与 Schema 指纹
    Arguments: input.Arguments,
    Reason:    input.Reason,
}
```

模型不能自己提交 Target 的版本或 SchemaHash。InteractionService 负责创建待审批记录、等待真实答复及生成审批指纹；实际 Shell 执行仍由 Registry 消费一次性票据，再进入执行网关。

共享的 Metadata/Descriptor 移到 `domain/tool.go`，Registry 通过类型别名保留原有使用方式。这样 InteractionService 不需要反向依赖 tool 适配器包。Descriptor JSON 的字段与顺序保持不变，避免无意义地改变已有审批指纹。

## 8. 为什么仍有 AgentToolRepository、AgentPlan 等名称

它们是既有持久化接口和数据库模型的业务前缀，不代表另一种可执行工具。此次没有重命名数据表或改动 Repository 契约，避免把纯代码分层调整扩大成数据库迁移。

区分方法很简单：Tool 是执行适配器，Service 是业务，Repository 是持久化，Model 是表结构。不是只要类型名里出现 Tool 或 Agent，就必须实现 Tool 接口。

## 9. 如何阅读与验证

建议按以下顺序看代码：

1. `tool/plan.go`：最短的一条类型化工具调用。
2. `tool/adapter.go`：泛型参数解码与结果编码。
3. `domain/tool.go`、`service/plan.go`：Go 参数与业务规则。
4. `tool/file.go`、`shell.go`：同样的适配方式，不同的底层依赖。
5. `tool/interaction.go`、`service/interaction.go`：工具目标解析与审批业务分离。
6. `tool/builtin.go`、`httpapi/server.go`：应用入口显式装配。

```bash
cd backend
go test ./...
go vet ./...
go test -race ./internal/tool ./internal/service ./internal/websearch

TEST_DATABASE_URL='你的测试数据库连接串' go test ./internal/repository \
  -run TestPostgresAgentToolsPersistenceApprovalAndCascade -count=1
```

新增适配器测试覆盖全部 12 个工具的参数映射、JSON 结果字段、人工等待超时、Schema 在调用 Service 前拒绝非法参数，以及审批元数据编码兼容。原有的越权、一次性审批、取消、文件版本校验和同步 Chat 人机交互测试继续保留。

本课没有修改环境变量、前端或数据库结构，也不新增真实模型/付费搜索调用。
