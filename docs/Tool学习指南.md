# Tool 学习指南：从原理、加载到新增工具与执行流程

这篇文档面向熟悉 Go 后端、正在学习 Agent 的开发者。它不是某个供应商 SDK 的教程，而是结合本项目当前实现，回答以下问题：

- 为什么已经有大模型，还需要 Tool？
- Tool 与普通 Go 方法、Service、Provider、RAG 有什么关系？
- 工具如何被“加载”，模型究竟拿到了什么？
- 用户发出消息以后，代码经过哪些函数才真正执行工具？
- 同步 Chat 和流式 ChatStream 分别怎么运行？
- 如何新增一个工具，并验证它真的接入了平台？
- 哪些安全、权限和持久化能力不能靠模型自行保证？

建议先读第 1～8 节建立整体认识，再跟着第 11 节做新增工具练习。所有项目代码路径均相对于仓库根目录；示例反映当前代码结构，不涉及 Go 插件动态加载或 MCP 自动发现。

## 目录

1. [先建立一个准确的心智模型](#1-先建立一个准确的心智模型)
2. [为什么要有 Tool](#2-为什么要有-tool)
3. [项目中的几个角色如何分工](#3-项目中的几个角色如何分工)
4. [一个 Tool 由哪些部分组成](#4-一个-tool-由哪些部分组成)
5. [启动时如何加载与注册 Tool](#5-启动时如何加载与注册-tool)
6. [一次对话如何获得可用工具](#6-一次对话如何获得可用工具)
7. [跟着计算器跑完一个完整循环](#7-跟着计算器跑完一个完整循环)
8. [对应到真实 Go 函数调用链](#8-对应到真实-go-函数调用链)
9. [同步和流式工具调用有什么不同](#9-同步和流式工具调用有什么不同)
10. [具体 Tool 为什么要薄，Service 为什么要类型化](#10-具体-tool-为什么要薄service-为什么要类型化)
11. [实战：新增一个文本长度工具](#11-实战新增一个文本长度工具)
12. [新增有数据库或外部 API 的工具](#12-新增有数据库或外部-api-的工具)
13. [用户提问和敏感操作审批](#13-用户提问和敏感操作审批)
14. [错误、超时、重试和执行边界](#14-错误超时重试和执行边界)
15. [日志与断点调试](#15-日志与断点调试)
16. [常见困惑与排查顺序](#16-常见困惑与排查顺序)
17. [阅读代码和动手学习路线](#17-阅读代码和动手学习路线)

## 1. 先建立一个准确的心智模型

一句话：**Tool 是服务端向模型公开的一种受控能力，模型提出调用请求，真正执行的是你的 Go 程序。**

例如用户说：“计算 `(18 + 24) * 7`。”

```text
用户提出任务
  → Go 服务把用户消息和可用工具说明发给模型
  → 模型返回：请求调用 calculator，参数是一个表达式
  → Go 服务验证并执行计算器
  → Go 服务把计算结果发回模型
  → 模型根据结果生成面向用户的回答
```

模型不会获得你服务器上的 Go 对象，也不会直接调用某个方法指针。网络两端传递的是工具说明、JSON 参数与执行结果。

对 Go 工程师来说，可以类比为：模型是一个“能够根据自然语言选择接口、构造请求”的客户端；Registry 是分派和安全入口；具体 Tool 是适配器；Service/Gateway 是真实能力实现。但这个客户端可能输出错误或不安全的请求，所以必须把它当作不可信输入源。

### 三件事一定要分开

| 阶段 | 实际发生的事 | 不代表什么 |
| --- | --- | --- |
| 工具已注册 | Go 进程保存了定义、版本、Schema 和执行器 | 不代表所有用户都能用 |
| 工具已向本轮模型开放 | 本轮请求的 tools 数组里有该工具说明 | 不代表模型一定调用 |
| 模型请求了工具，服务端执行成功 | 收到 tool_calls，且通过校验和执行 | 不代表最终回答已经生成 |

“为什么注册了却没执行”通常就是混淆了这三个阶段。

## 2. 为什么要有 Tool

### 2.1 语言生成不等于业务执行

模型回答“我已经创建了报告”，不等于数据库里真的出现了报告记录。

```text
纯聊天：生成“我已完成”这句话。
工具调用：业务服务完成保存，返回真实 artifactId，再让模型说明结果。
```

同理，模型写出一段 Shell 命令，不等于命令已经执行。只有服务端执行器实际运行并返回结果，才发生了真实操作。

### 2.2 补齐模型无法独立完成的能力

| 需求 | 为什么只靠模型不够 | 本项目的 Tool |
| --- | --- | --- |
| 精确计算 | 需要确定性的计算结果 | calculator |
| 浏览或修改会话文件 | 模型不能自行读取服务端工作区 | file.list/read/write/apply_patch/search |
| 执行程序 | 需要真实进程、资源限制与退出码 | shell.exec |
| 检索外部资料 | 需要实际调用检索服务 | web.search |
| 记录任务进度 | 需要持久化、可查看的计划 | plan.update |
| 等待用户补充信息 | 不能让模型虚构用户答复 | user.ask |
| 获取真实批准 | 不能让模型给自己授权 | user.request_approval |
| 保存可交付的内容 | 需要实际存储与后续读取 | artifact.save/read |

### 2.3 Tool 不是把大模型换掉

分工通常是：

- 模型：理解意图、选择合适能力、组织参数、理解返回结果、生成回答。
- Go 程序：鉴权、校验、业务规则、实际执行、落库、审计与资源控制。

工具结果不一定是最终答案。例如搜索工具返回若干网页片段，模型仍需根据资料组织回答；计划工具返回保存后的计划，模型仍需继续推进任务。

### 2.4 不是所有功能都应该做成 Tool

如果流程是固定的，例如“每次登录都检查账号状态”，应直接写在正常业务流程里，不需要询问模型是否检查。

只有在任务推进中，需要模型根据上下文决定“是否使用、使用哪个能力、传什么参数”时，把它开放为 Tool 才有意义。

也不是所有工具都要落库：计算器可以是纯计算；网络检索可调用外部接口；计划、审批、产物则需要业务状态与持久化。

## 3. 项目中的几个角色如何分工

| 角色 | 当前代码位置 | 负责什么 |
| --- | --- | --- |
| ChatService | [service/chat.go](../backend/internal/service/chat.go) | 对话编排、模型/工具循环、最终消息保存 |
| LLMGateway | [llmgateway/gateway.go](../backend/internal/llmgateway/gateway.go) | 统一模型调用、Token 估算、限流、幂等、重试与观测 |
| Provider | [provider/provider.go](../backend/internal/provider/provider.go) | 将统一请求翻译成供应商协议，解析模型响应 |
| Tool Registry / ToolSet | [tool/tool.go](../backend/internal/tool/tool.go)、[registry.go](../backend/internal/tool/registry.go) | 工具注册、快照、权限、Schema、审批门禁与审计 |
| 具体 Tool | [tool/file.go](../backend/internal/tool/file.go) 等 | JSON 参数与 Go 方法之间的薄适配器 |
| Service | [service/plan.go](../backend/internal/service/plan.go) 等 | 所有权与业务规则、状态流转、持久化编排 |
| execution.Gateway | [execution/gateway.go](../backend/internal/execution/gateway.go) | 工作区隔离、文件保护和受限 Docker 执行 |
| Repository / Model | [repository/agent_tool.go](../backend/internal/repository/agent_tool.go)、[model/agent_tool.go](../backend/internal/database/model/agent_tool.go) | 数据库操作与表结构 |

特别注意：**LLMGateway 与 execution.Gateway 不是同一个网关。前者调用模型，后者操作隔离工作区和沙箱。**

### 与普通函数、API、RAG、Prompt 的区别

- 普通 Go 函数：真实实现，不会仅因为函数存在就被模型看见。
- HTTP API：网络调用入口；Tool 可以复用背后的业务服务，不需要为了复用而在进程内再发一遍 HTTP。
- Tool：模型可选择的能力契约及服务端适配器，可以调用普通函数、Service、数据库或外部 API。
- Prompt：告诉模型如何行动，不能替代服务端权限或执行器。
- RAG：检索资料并增强模型上下文；可以作为固定业务流程，也可以包装成模型按需选择的检索 Tool。当前项目的知识库检索与本篇的工具循环不是同一条自动等价链路。

本项目的 Agent 行为主要体现在 ChatService 的循环编排中，并没有另一个神秘的运行时帮忙执行所有工具。

## 4. 一个 Tool 由哪些部分组成

### 4.1 Tool 接口：能描述，也能执行

当前接口位于 `backend/internal/tool/tool.go`：

```go
type Tool interface {
    Definition() provider.ToolDefinition
    Execute(ctx context.Context, arguments string) (string, error)
}
```

两部分用途不同：

- Definition：告诉模型这个工具叫什么、用来干什么、参数格式是什么。
- Execute：保留在服务端的真实入口，收到已校验的参数后执行。

`Execute` 返回 string 是协议边界的表示方式，不代表业务服务应该到处接收字符串。项目中的 `executeTyped` 会将 JSON 转成 Go 结构体，再调用类型明确的方法。

### 4.2 Definition：模型实际看到的说明书

以计算器为例，发送结构大致如下：

```json
{
  "type": "function",
  "function": {
    "name": "calculator",
    "description": "计算基础算术表达式",
    "parameters": {
      "type": "object",
      "properties": {
        "expression": { "type": "string" }
      },
      "required": ["expression"],
      "additionalProperties": false
    }
  }
}
```

这是用于讲解的简化说明，实际描述以 `tool/calculator.go` 为准。

- name：模型返回调用请求时使用的名称。
- description：帮助模型判断何时使用。说明不清楚，可能影响模型选择工具，但不能放宽服务端权限。
- parameters：参数的 JSON Schema，不是参数本身，更不是 Go 代码。

这里的 Schema 要求参数是对象，必须有 expression，且不能增加其他字段。它约束的是“怎样请求工具”，不同于最终回答的 `ResponseFormat` Schema。

模型能看见说明书，**看不见 Execute 的 Go 实现、数据库连接、服务对象或密钥**。也不应把这些秘密写进工具描述或结果。

### 4.3 Metadata：服务端管理信息

共享类型位于 `backend/internal/domain/tool.go`，tool 包通过类型别名使用。

以文件读取工具为例：

```text
Namespace   = file
Name        = read
Version     = 1.0.0
Source      = builtin
Category    = file
WireName    = file_read
```

对应三个不同层次的名称：

- `file.read`：文档中的逻辑名称。
- `builtin:file.read@1.0.0`：带来源和版本的服务端身份。
- `file_read`：当前供应商协议实际使用的 WireName。

不要把文档里的 `file.read` 直接当作当前调用参数中的函数名。

Metadata 方法是 Registry 支持的可选能力，不是 Tool 接口的必选方法。旧工具没有 Metadata 时会使用 legacy 默认身份；新内置工具应显式提供，避免丢失清晰的版本与来源信息。

Category、Source、Exposure、Capabilities 是描述信息。**当前这些字段并不会自动变成完整权限系统**；真正的授权取决于 Policy、审批门禁和执行网关。不能只写一个 `Capabilities: ["read"]` 就认为服务端已经禁止其他行为。

### 4.4 Descriptor：用于固定审批身份

Descriptor 在 Metadata 之外增加 SchemaHash。审批时需要同时知道“是哪一个版本、哪一份参数契约”，不能只批准一个易变的名称。

当前内置工具通常嵌入 toolSpec，复用 Definition、Metadata 与可选超时，然后独立实现 Execute。

## 5. 启动时如何加载与注册 Tool

### 5.1 当前的“加载”是显式装配，不是扫描目录

当前项目没有以下机制：

- 自动扫描 tool 目录，把所有 Go 文件自动变成工具。
- 从数据库读取 Go 代码并执行。
- 加载 `.so` 动态插件。
- 自动发现 MCP Server 或读取某个 skill 文件后注册工具。

**Go 文件被编译进入程序，只代表代码存在；必须创建工具对象并交给 Registry，才完成运行时注册。**

### 5.2 启动入口

`backend/cmd/api/main.go` 读取配置、初始化依赖，并创建可选执行网关：

```go
executor, err := execution.New(cfg.Execution, nil)
// 处理错误……
server := httpapi.NewServerWithExecutionGateway(
    // 其他依赖……
    executor, cfg,
)
```

上面是只保留关键关系的伪代码，不是完整可复制函数调用。

`EXECUTION_ENABLED=false` 时，execution.New 返回 nil；不会为了关闭的能力创建工作区或连接 Docker。

### 5.3 应用层组装具体业务能力

核心装配位于 `backend/internal/httpapi/server.go` 的 `NewServerWithExecutionGateway`。下面也是关键语句节选：

```go
plans := service.NewPlan(repositories.AgentTools, repositories.Conversations, userEvents)
interactions := service.NewInteraction(
    repositories.AgentTools, repositories.Conversations,
    userEvents, cfg.AgentTools.HumanTimeout,
)
artifacts := service.NewArtifact(repositories.AgentTools, repositories.Conversations, userEvents)

tools := tool.NewDefaultRegistry(tool.NewBuiltinTools(tool.BuiltinDependencies{
    Execution:    executor,
    Plans:        plans,
    Interactions: interactions,
    Artifacts:    artifacts,
    Search:       search,
    HumanTimeout: cfg.AgentTools.HumanTimeout,
})...)

tools.SetApprovalGuard(interactions)
```

search 只在启用联网搜索配置时创建。之后同一函数还会 Configure 工具策略、大小和超时，并把 tools 注入 ChatService。

这里 `...` 是 Go 的切片展开语法：NewBuiltinTools 返回 `[]Tool`，NewDefaultRegistry 接收 `...Tool`。

### 5.4 NewBuiltinTools 根据依赖选择装配哪些工具

对应代码：`backend/internal/tool/builtin.go`。

| 条件 | 装配结果 |
| --- | --- |
| Execution 非 nil | Shell + 五个文件工具 |
| Plans 非 nil | plan.update |
| Interactions 非 nil | user.ask、user.request_approval |
| Artifacts 非 nil | artifact.save、artifact.read |
| Search 非 nil | web.search |
| NewDefaultRegistry | 额外加入 calculator |

因此，标准装配下：

- 执行与搜索都关闭：calculator 加五个计划/交互/产物工具，共 6 个。
- 执行与搜索都开启：12 个 MVP 工具加 calculator，共 13 个。

这是进程注册数量，不是任意用户、任意模型最终一定看见的数量。

### 5.5 Register 做了什么

`Registry.Register` 的核心工作：

1. 获取 Definition 和 Metadata，检查名称等元数据格式。
2. 编译 JSON Schema，错误定义不能悄悄继续工作。
3. 复制定义，计算 Schema 指纹。
4. 拒绝重复的“逻辑身份 + 版本”。
5. 拒绝不同工具占用同一 WireName，拒绝同一逻辑工具的不同版本改变 WireName。
6. 保存执行器；首次注册的版本成为该工具的活跃版本。

Registry 中既保存模型说明，也保存服务端执行对象。模型拿到说明后，后端仍能根据 WireName 找到对应执行器。

注册新版本不会根据语义版本号自动选择“最新”。当前需要由服务端显式 `Activate(id, version)`；没有面向网页的热加载管理接口。通常修改代码后需要重新构建/重启后端。

## 6. 一次对话如何获得可用工具

### 6.1 先看 Provider 的当前适配能力

ChatService.Prepare 中有这段关键逻辑：

```go
if s.gateway.SupportsTools(input.Provider) && s.tools != nil {
    session.toolSet = s.tools.SnapshotForUser(ctx, userID)
    request.Tools = session.toolSet.Definitions()
}
```

当前 `provider.Registry.SupportsTools` 只对配置类型为 `openai-compatible` 的 Provider 返回 true。这描述的是**本项目当前实现**，不是声称其他厂商的模型没有工具能力；例如当前 Anthropic 适配路径尚未接入这套工具协议。

另外，Provider 被判断为兼容，并不保证其每个具体模型都实际支持这份协议。实际兼容性仍需要模型侧支持与测试。

### 6.2 SnapshotForUser 做权限过滤

它复制当前活跃工具，再通过 Policy 判断该用户能否看到每个工具。权限查询出错或拒绝时，不把相应定义放进该用户的快照。

应用装配使用 RolePolicy：`TOOL_ADMIN_ONLY` 中的工具仅管理员可用；如果 `EXECUTION_ADMIN_ONLY=true`，六个执行工具也加入管理员限制。

### 6.3 为什么把 ToolSet 放进 ChatSession

假设模型看到了 v1 的参数说明，但执行前全局 Registry 切换到 v2。若重新查全局活跃版本，可能出现“模型按旧协议请求，服务端用新实现执行”。

因此，Prepare 为这次业务消息固定 ToolSet，后面所有模型/工具轮次都使用它。重新生成和编辑消息的 Prepare 路径也有相同处理。

快照固定定义和执行器，但不是永久权限凭证：实际执行时 Registry 仍会再次检查 Policy。

### 6.4 供应商收到什么

本项目的 OpenAI 兼容适配器把 `request.Tools` 转成 HTTP 请求里的 tools 数组；非空时使用 `tool_choice: "auto"`。

auto 的含义是允许模型根据上下文选择调用，也允许直接回答。它不是“遍历执行所有注册工具”。当前业务入口没有提供任意指定工具清单或强制工具选择的通用网页参数。

工具描述、Schema、参数和返回结果都会占用模型上下文。LLMGateway 的估算也会考虑这些数据，所以不是工具越多越好；应公开必要的能力，控制定义与结果大小。

## 7. 跟着计算器跑完一个完整循环

下面是一个可能发生的对话过程，示例工具调用 ID 与结果仅用于讲解，不是一次真实线上日志。

### 第一步：用户消息和工具说明进入模型请求

用户说：

```text
请使用 calculator 计算 (18 + 24) * 7。
```

初始 messages 中有 system、历史消息和本次 user 消息；tools 中包含该用户本轮可用的计算器定义。

### 第二步：模型返回调用请求

```json
{
  "role": "assistant",
  "content": "",
  "tool_calls": [
    {
      "id": "call_calc_1",
      "type": "function",
      "function": {
        "name": "calculator",
        "arguments": "{\"expression\":\"(18 + 24) * 7\"}"
      }
    }
  ]
}
```

注意 arguments 在外层协议里是一个 JSON **字符串**。里面的内容解析后才是对象：

```json
{ "expression": "(18 + 24) * 7" }
```

这时仅仅是“请求调用”，计算尚未完成。

### 第三步：服务端验证并执行

Registry/ToolSet 确认工具本轮可用、用户有权限、参数合规，然后调用 calculator.Execute，得到：

```json
{ "result": 294 }
```

### 第四步：把请求和结果一起追加到 messages

```json
[
  { "role": "user", "content": "请使用 calculator 计算 (18 + 24) * 7。" },
  {
    "role": "assistant",
    "content": "",
    "tool_calls": [
      {
        "id": "call_calc_1",
        "type": "function",
        "function": {
          "name": "calculator",
          "arguments": "{\"expression\":\"(18 + 24) * 7\"}"
        }
      }
    ]
  },
  {
    "role": "tool",
    "tool_call_id": "call_calc_1",
    "content": "{\"result\":294}"
  }
]
```

为了突出工具闭环，上面省略了 system 和历史消息。

- assistant 的 tool_calls 表达模型刚才请求了什么。
- tool 消息表达服务端实际执行的结果。
- tool_call_id 必须与调用 ID 对应，用来关联结果，不是数据库里的 assistant 消息 ID。
- 一个业务消息可包含多个模型调用轮次，每轮又可包含多个 tool_calls。

### 第五步：再次调用模型

Go 程序把更新后的 messages 再发给模型。模型看到 294，生成“计算结果是 294”等最终回答。如果又请求工具，继续循环；没有工具调用时，正常结束这一轮业务生成。

**工具执行结束，不等于整个 Agent 回答结束。工具结果需要回到模型上下文，才构成完整闭环。**

## 8. 对应到真实 Go 函数调用链

### 8.1 HTTP 到会话准备

流式发送消息的主要路径是：

```text
Gin 路由：POST /api/v1/conversations/:conversationID/messages
  → handler.StreamMessage
  → prepareChatSession
  → ChatService.Prepare
      校验输入、用户/会话、模型选择，准备消息和上下文
      创建 user/assistant 消息
      SnapshotForUser → 固定 ToolSet → request.Tools
  → streamPreparedSession
  → ChatService.Stream
```

这只是关注工具的主干，额度、审核、附件、知识库等原有逻辑仍会参与 Prepare。用户身份由鉴权层提供，不由模型工具参数决定。

### 8.2 一轮模型调用

```text
ChatService.Stream
  → consumeProviderStream
  → LLMGateway.ChatStream
  → 选中的 Provider.ChatStream
  → 供应商 HTTP 接口
```

业务层依然统一走 LLMGateway，不会因为增加工具就绕开 Token 预算、限流和观测。

### 8.3 模型返回 ToolCalls 后

```text
Provider 解析得到完整 ToolCalls
  → consumeProviderStream 收集到本次模型响应
  → ChatService.Stream 判断是否有工具调用
  → executeToolCalls
      写入服务端 Scope
      追加 assistant(tool_calls) 消息
      逐个调用 session.toolSet.Execute
  → Registry.execute
      再次鉴权、参数大小、Schema、超时、审批
  → 对应具体 Tool.Execute
  → Service/Gateway
  → 返回结果，追加 role=tool 消息
  → 下一轮 LLMGateway.ChatStream
```

当前 executeToolCalls 使用普通 for 循环，**同一批 tool_calls 按顺序执行，不是自动并行**。模型一次返回多个调用，不等于这些操作就适合并发。

### 8.4 Scope 解决“代表谁执行”

executeToolCalls 将用户 ID、会话 ID 和工具任务身份放入 Context：

```go
toolOperationID := fmt.Sprintf("%x", sha256.Sum256(
    []byte(session.OperationID+":"+session.AssistantMessage.ID),
))
ctx = execution.WithScope(ctx, execution.Scope{
    UserID:         session.UserID,
    ConversationID: session.UserMessage.ConversationID,
    OperationID:    toolOperationID,
})
```

其意义是：

- 模型只提交 path、content、command 等业务参数，不能自己传 userID 获得另一个人的工作区。
- 计划/产物服务可以检查当前会话所有权。
- 审批票据绑定这一轮业务消息；客户端不传幂等头时也有服务端任务身份。
- Context 继续携带取消和 deadline，工具不能随意脱离请求继续运行。

### 8.5 生成结束与保存

最终内容和用量由 ChatService 原有终态保存路径写入数据库，Handler 把完成/中断等结果发给浏览器。

不要据此认为所有中间工具轨迹也都完整落库：目前 tool_calls 和 role=tool 主要是本次循环中的上下文消息；计划、交互、产物有专门数据表，工具还有审计日志，但尚未实现完整可回放的逐次工具执行历史表。

## 9. 同步和流式工具调用有什么不同

### 9.1 同步 Chat

同步入口使用 `/messages/chat`，ChatService.Chat 每次等待 LLMGateway.Chat 返回完整 ChatResponse。若其中有 ToolCalls，执行并追加结果，再发下一次完整请求。最终整个回答完成后才返回普通 HTTP 响应。

同步并不代表只调用一次模型：一次用户消息完全可能经历“模型 → 工具 → 模型”。

### 9.2 流式 ChatStream

流式响应里的正文和参数都可能被切成片段。例如计算器参数可能先后收到：

```text
片段 1：{"expression":"18+
片段 2：24"}
```

第一段不是可执行的完整参数，不能收到一点就调用工具。

当前 OpenAI 兼容适配器在 `provider/openai_compatible.go` 中按工具调用 index 收集 ID、名称与 arguments 分片；收到正常的 `[DONE]` 后，发送完整 ToolCalls 和该次模型调用的 Done 事件。缺少正常结束标记的 EOF 会视为断流，不把半截调用当作成功执行。

ChatService 收到完整调用后再执行工具，然后发起下一轮模型流。最终正文的增量继续通过原有 SSE 返回浏览器。

### 9.3 不要混淆两种“完成”和两个事件层次

- Provider 的 Done：这**一次模型 HTTP 调用**结束，下一步可能还要运行工具。
- 网页收到的业务 done：ChatService 已结束整次生成并保存终态。
- 主消息 SSE：用于正文、思考与消息终态。
- 用户事件 SSE：例如 agent.state.changed，提示浏览器读取最新计划、提问、审批或产物状态。

Registry 不负责拼供应商 SSE 分片，也不负责向网页直接发送正文；这是 Provider、ChatService 与 Handler 的职责。

## 10. 具体 Tool 为什么要薄，Service 为什么要类型化

以 `plan_update` 为例：

```go
type PlanUpdater interface {
    Update(ctx context.Context, input domain.UpdatePlanInput) (domain.Plan, error)
}

func (t *PlanUpdateTool) Execute(ctx context.Context, args string) (string, error) {
    return executeTyped(ctx, args, t.plans.Update)
}
```

实际业务落在 PlanService.Update 中：检查会话所有权、步骤数量、最多一个进行中步骤，保存并返回计划。

### 10.1 executeTyped 是什么

位于 `tool/adapter.go`：

```go
func executeTyped[Input, Output any](
    ctx context.Context,
    raw string,
    execute func(context.Context, Input) (Output, error),
) (string, error)
```

它做四件事：

1. 检查取消，要求单个 JSON 对象。
2. 严格解码为 Input，拒绝未知字段和多余尾随对象。
3. 调用类型明确的 execute 方法。
4. 把 Output 编码成 JSON 字符串。

Go 会根据 `t.plans.Update` 推导出 Input 与 Output。它不按工具名 switch，也不是另一套 Registry。

### 10.2 为什么 Service 不应该是 ExecuteTool(name, rawJSON)

因为那会把模型传输协议扩散进业务层，并把很多不相关的业务塞进一个字符串分派器。

现在的 PlanService、InteractionService、ArtifactService 都接收明确的 Go 类型。工具侧定义较小的依赖接口；Service 通过 Go 的隐式接口实现被注入，不需要反向依赖具体 Tool。

### 10.3 多层校验不是都要删除

- Schema：参数对象是否符合公开契约。
- 严格解码：能否正确转成 Go 类型，避免字段被静默忽略。
- Service：是否满足业务规则。
- Gateway：路径、容量、沙箱、版本条件是否安全。

例如，path 是一个合法字符串，并不能证明它没有路径穿越；JSON 格式正确，也不能证明用户有权访问某个会话。

## 11. 实战：新增一个文本长度工具

以下是完整的学习练习，**本篇文档不会自动把这个工具加入当前运行环境**。现有代码尚未新增 text_length；需要你按步骤创建文件、装配、测试并重启后端。

选择一个纯计算工具作为练习，可以先避开数据库和外部 API，专注理解接入方式。

### 11.1 定义边界

- 逻辑名：text.length。
- WireName：text_length。
- 参数：text，一个不超过 4096 个 Unicode 字符的字符串。
- 结果：Unicode 码点数量 characters、UTF-8 字节数量 bytes。
- 无外部副作用，不需要数据库、网络或逐次审批。

这里 characters 按 Go rune 计数，不等于屏幕显示的字素数量，也**不是 Token 数量**。例如部分 Emoji 可能由多个码点组成。

### 11.2 创建具体工具

新建 `backend/internal/tool/text.go`，放入下列完整代码：

<!-- example:text.go -->
```go
package tool

import (
    "context"
    "unicode/utf8"
)

type textLengthInput struct {
    Text string `json:"text"`
}

type textLengthOutput struct {
    Characters int `json:"characters"`
    Bytes      int `json:"bytes"`
}

type TextLengthTool struct {
    toolSpec
}

func NewTextLength() *TextLengthTool {
    spec := newSpec(
        "text", "length", "text",
        "统计文本的 Unicode 码点数与 UTF-8 字节数。不是 Token 估算。",
        `{"type":"object","properties":{"text":{"type":"string","maxLength":4096}},"required":["text"],"additionalProperties":false}`,
        "compute",
    )
    return &TextLengthTool{toolSpec: spec}
}

func (t *TextLengthTool) Execute(ctx context.Context, arguments string) (string, error) {
    return executeTyped(ctx, arguments, measureTextLength)
}

func measureTextLength(_ context.Context, input textLengthInput) (textLengthOutput, error) {
    return textLengthOutput{
        Characters: utf8.RuneCountInString(input.Text),
        Bytes:      len(input.Text),
    }, nil
}
```

为什么只需要实现 Execute？因为嵌入的 toolSpec 已经提供 Definition、Metadata 和可选执行超时。Schema 仍由 Registry 统一编译与校验。

本例计算有严格输入上限且很短。实际新增长循环、网络请求或进程工具时，仍需要在底层操作中正确响应 Context，不能只依赖入口检查。

### 11.3 把对象加入装配

修改 `backend/internal/tool/builtin.go` 中 NewBuiltinTools 的起始切片：

```go
// 原来是：items := []Tool{}
items := []Tool{NewTextLength()}
```

它是没有额外依赖的工具，所以可以直接装配。原有 NewDefaultRegistry 会继续把这份切片加入统一 Registry，不需要新增注册表，也不需要修改 ChatService 的工具循环。

至此才形成了“代码存在 → 对象创建 → 注册进入运行时”的完整链路。

如果工具需要自己的依赖或启用开关，则不要无条件创建，参照第 12 节通过 BuiltinDependencies 与应用入口注入。

### 11.4 编写不依赖真实模型的测试

新建 `backend/internal/tool/text_test.go`：

<!-- example:text_test.go -->
```go
package tool

import (
    "context"
    "encoding/json"
    "strings"
    "testing"

    "github.com/example/agent-platform/backend/internal/provider"
)

func textLengthCall(arguments string) provider.ToolCall {
    return provider.ToolCall{
        ID:   "call_text_1",
        Type: "function",
        Function: provider.ToolFunctionCall{
            Name:      "text_length",
            Arguments: arguments,
        },
    }
}

func TestTextLengthToolGuide(t *testing.T) {
    registry := NewRegistry(NewTextLength())

    raw, err := registry.ExecuteForUser(
        context.Background(), "user-1",
        textLengthCall(`{"text":"你好Go"}`),
    )
    if err != nil {
        t.Fatal(err)
    }

    var result textLengthOutput
    if err := json.Unmarshal([]byte(raw), &result); err != nil {
        t.Fatal(err)
    }
    if result.Characters != 4 || result.Bytes != 8 {
        t.Fatalf("统计结果错误: %+v", result)
    }

    for _, arguments := range []string{
        `{}`,
        `{"text":123}`,
        `{"text":"ok","userID":"other-user"}`,
        `{"text":"one"} {"text":"two"}`,
    } {
        if _, err := registry.ExecuteForUser(
            context.Background(), "user-1", textLengthCall(arguments),
        ); err == nil {
            t.Fatalf("非法参数未拒绝: %s", arguments)
        }
    }

    oversized, err := json.Marshal(textLengthInput{Text: strings.Repeat("a", 4097)})
    if err != nil {
        t.Fatal(err)
    }
    if _, err := registry.ExecuteForUser(
        context.Background(), "user-1", textLengthCall(string(oversized)),
    ); err == nil {
        t.Fatal("超过 Schema 长度上限时应当拒绝")
    }
}
```

运行：

```bash
cd backend
go test ./internal/tool -run '^TestTextLengthToolGuide$' -count=1
```

这里通过 Registry 测试，而不是只直接调用 TextLengthTool.Execute，才能覆盖真正的模型调用边界和 Schema 校验。本工具是纯计算，所以单测不需要用户会话数据库或执行工作区 Scope。

### 11.5 再验证装配与整条对话链

还需要验证：

1. NewBuiltinTools 的结果中存在 text_length。
2. SnapshotForUser 的 Definitions 中有它。
3. 选择当前项目已接入工具协议的 Provider 与实际支持工具调用的模型。
4. 重新构建/重启后端，新发一条消息：“请务必调用 text_length 统计 `你好Go`，然后解释 characters 与 bytes。”
5. 查看日志确认实际收到并执行 text_length，不要只根据模型说“我调用了”判断成功。

模型仍使用 auto，因此自然语言要求不等于服务端强制调用。可用伪 Provider 返回固定 ToolCalls 来做确定性的服务集成测试，参照 `service/chat_execution_test.go`。

新增默认工具后，注册数量会变化。当前某些测试对 NewBuiltinTools 的数量有断言，需要同步更新；特别是“空依赖返回空切片”的旧预期。更稳妥的装配测试是检查特定名称存在/不存在，而不只检查总数。

完成新增能力时，还应补工具说明和测试；若引入环境配置，同步修改 `.env`、`.env.example` 并使用中文注释。

## 12. 新增有数据库或外部 API 的工具

纯计算练习不需要为了“有分层”而创建空的 Service。涉及业务状态时，才引入明确的业务服务。

### 推荐步骤

1. 在 domain 中定义类型明确的输入/输出。
2. 在 service 中实现业务规则；数据库查询仍放在 repository。
3. 在 tool 中定义需要的最小依赖接口与具体适配器。
4. 在 BuiltinDependencies 中增加依赖，并根据非 nil 条件装配。
5. 在应用入口创建真实实现、注入依赖，继续交给同一个 Registry。
6. 增加参数映射、权限、错误、超时和业务/持久化测试。

以已有计划工具的类型关系为例：

```text
PlanUpdateTool
  持有 PlanUpdater 接口
    ↑ Go 隐式接口实现
PlanService.Update(ctx, domain.UpdatePlanInput)
  → Repository.SavePlan
```

工具适配器不需要 import 具体 PlanService，也不会在 Execute 里拼 SQL。

### 安全设计问题要先问清楚

- 谁能调用？是否仅管理员？
- 操作哪个用户、哪个会话的数据？身份应从哪里取得？
- 参数能否指定任意文件路径、网络地址、SQL 或命令？
- 是只读、写入还是可能产生不可逆副作用？是否需要审批？
- 如何限制输入、响应、并发和执行时长？
- 重复调用会怎样？是否需要业务唯一键或幂等约束？
- 外部返回内容是否可能包含提示注入或敏感信息？

对于外部 API，继续把网络客户端封装成明确接口，传入 Context、限制地址与响应体、脱敏错误。不要让模型随意指定 URL，再用携带服务端凭证的客户端直接访问。

若新增需要用户审批的工具，必须确保 Registry 可以消费审批票据；不能在工具内部接受 `approved: true` 之类的模型自报字段来绕过授权。

## 13. 用户提问和敏感操作审批

### 13.1 user.ask 不是一段展示文案

当前实现会创建 pending 交互记录，通过 agent.state.changed 通知浏览器。用户在卡片中提交后，鉴权 HTTP 接口更新记录；InteractionService 等到真实响应，返回 answer，再交给模型继续对话。

模型不能通过“编一句用户答复”替代这次状态更新。

### 13.2 审批与执行是两次动作

```text
模型请求 user_request_approval
  → 工具从本轮快照解析目标身份与 Schema
  → 创建待审批记录
  → 用户点击批准/拒绝
  → 审批工具把真实选择返回模型

模型随后请求 shell_exec
  → Registry 再次鉴权、校验参数
  → InteractionService.Authorize 消费匹配的一次性票据
  → ShellExecTool → execution.Gateway → Docker
```

user_request_approval 自己不会执行目标命令。批准也不是永久白名单；当前票据绑定用户、会话、工具任务身份、目标版本、Schema 与完整参数。换命令、换任务或重复消费都会失败。

工作区文件工具与 Shell 也不是等价权限：文件操作被限制在隔离工作区并有版本条件，当前 Shell 额外要求逐次审批。对未来新增工具，应按风险明确设计策略，而不是只看业务分类。

### 13.3 不要为了缩短链路绕过 Registry

直接调用 `NewShellExec(...).Execute(...)` 不会替你完成 Registry 的权限与审批门禁。生产工具调用应走本轮 ToolSet；单测底层 Gateway 是为了验证基础设施，不是提供新的未授权入口。

## 14. 错误、超时、重试和执行边界

### 14.1 工具报错之后会怎样

executeToolCalls 会将工具 error 转成类似下面的结果，再作为 role=tool 消息追加：

```json
{ "error": "工具参数不符合服务端 JSON Schema" }
```

模型可能据此纠正参数、换方案或解释无法完成；不是每次工具报错都立即结束整次对话。上下文取消、总超时、模型流异常、轮次超限等则由外层生成流程处理。

新增工具应返回可理解、脱敏的错误，不要 panic，也不要把密钥、完整内部请求头或数据库敏感内容当成错误文案回传模型。

### 14.2 当前主要限制

下面是当前默认配置，可在 `.env.example` 对照：

| 配置 | 默认值 | 作用 |
| --- | --- | --- |
| MAX_TOOL_ITERATIONS | 8 | 最多执行多少轮模型提出的工具请求 |
| TOOL_ARGUMENT_MAX_BYTES | 65536 | 单次调用参数字节上限 |
| TOOL_RESULT_MAX_BYTES | 32768 | 单次结果字节上限，超过返回错误，不截坏 JSON |
| TOOL_EXECUTION_TIMEOUT | 30s | 普通工具执行时长上限 |
| TOOL_HUMAN_TIMEOUT | 90s | 用户提问/审批的专用等待上限 |
| STREAM_TOTAL_TIMEOUT | 5m | 当前流式整次业务的总时长上限 |
| EXECUTION_ENABLED | false | 是否创建文件/Shell 执行能力 |
| EXECUTION_ADMIN_ONLY | true | 执行工具是否仅管理员可用 |
| WEB_SEARCH_ENABLED | false | 是否创建联网搜索能力 |

MAX_TOOL_ITERATIONS 统计的是**工具调用轮次**，不是单个函数的总执行次数。一轮模型响应可返回多个 ToolCalls，当前会逐个串行执行。因此不能把这个配置误认为完整的累计调用量/费用配额。

人工等待的专用超时仍受父 Context 剩余时长限制。同步 Chat 继承其调用 Context，也有各工具的超时；不要把 Stream 方法内部创建的总时长计时器想当然地等同于同步方法也创建了同一计时器。

### 14.3 取消不是只停止网页显示

工具必须继续传递 ctx。用户停止生成等动作导致原请求 Context 取消时，工具等待、网络请求或沙箱执行应及时结束；交互服务还会尝试把未完成记录改为 canceled。

不要随意在工具里新建无关联的 context.Background 并启动后台 goroutine，否则可能出现“网页停了，后台操作继续”的问题。必要清理任务可以使用独立、短时限 Context，当前交互取消清理就是这种受限用途。

### 14.4 模型重试不等于业务副作用幂等

LLMGateway 的模型请求重试、整次消息幂等记录、工具审批一次性消费，是不同层次的机制。

- 供应商调用的一次内部重试，不应该被当作一条新的用户业务消息。
- 模型在下一轮再次请求某工具，可能构成一次新的工具执行。
- tool_call_id 是关联请求与结果的 ID，不会自动替代数据库唯一键或业务幂等设计。
- 一次性审批是执行门禁，不保证对所有外部系统实现端到端 exactly-once。

尤其是“写入已成功，但返回结果失败或超限”的情况，不能简单无脑重试副作用。新增付款、发消息等高风险工具时，需要额外的幂等键、状态查询和补偿设计；本项目当前 MVP 没有自动解决所有这些问题。

## 15. 日志与断点调试

### 15.1 推荐断点

| 顺序 | 函数 | 重点观察 |
| --- | --- | --- |
| 1 | NewBuiltinTools | 对应依赖是否非 nil，实际创建了哪些 Tool |
| 2 | Registry.Register | WireName、版本、Schema 是否注册成功 |
| 3 | ChatService.Prepare | 选中哪个 Provider，是否进入 SupportsTools 分支 |
| 4 | SnapshotForUser | 目标工具是否被权限过滤 |
| 5 | ToolSet.Definitions / request.Tools | 模型实际收到哪些说明 |
| 6 | Provider 的 Chat/ChatStream | 是否返回 ToolCalls，流式参数是否拼完整 |
| 7 | ChatService.executeToolCalls | call.ID、名称、参数和服务端 Scope |
| 8 | ToolSet.Execute / Registry.execute | 是否被快照、权限、Schema、审批或大小限制拒绝 |
| 9 | 具体 Tool.Execute、Service/Gateway | Go 参数与实际操作结果 |
| 10 | 下一轮模型请求 | messages 是否包含对应 assistant 调用与 role=tool 结果 |

学习时可以在断点查看参数；但生产日志不应无限记录原始参数和敏感内容。

### 15.2 当前审计能提供什么

关注日志消息：

```text
tool execution started
tool execution audit
tool execution completed
tool execution failed
```

实际执行的审计字段包括 user_id、conversation_id、tool_operation_id、tool_call_id、tool_name、tool_id、tool_version、tool_source、tool_category、schema_hash、argument_bytes、argument_digest、status、duration_ms、result_bytes。

部分拒绝发生在执行前，会输出各自的拒绝日志，不要假设每次拒绝都有一条成功进入执行后的 audit。还可以结合原有 request_id 和 LLMGateway 观测定位整条链路。

如果日志只有模型调用，没有 tool execution started，先确认模型是否真的返回 ToolCalls，而不是立刻怀疑执行器。

## 16. 常见困惑与排查顺序

### 新增 Go 文件后为什么模型没看见？

文件不会自动注册。检查对象是否加入 NewBuiltinTools、依赖是否注入、是否重启后端，再检查 Provider 能力与用户权限。

### 工具已在 Registry，为什么本轮不开放？

检查 SupportsTools、SnapshotForUser 和 Policy。进程注册数量与用户快照数量不是同一件事。

### tools 数组里已经有它，为什么仍然直接回答？

当前 tool_choice 是 auto。检查工具描述和用户任务是否匹配；用固定 ToolCalls 的伪 Provider 区分“模型没有选择”与“执行链路错误”。

### 模型能否指定未注册的函数，例如任意系统命令执行器？

模型可以输出错误名称，但服务端只按当前快照匹配，没有就拒绝。不能因为模型说需要某能力，就动态执行任意 Go 函数或任意宿主命令。

### 已注册多个版本，会自动选最大版本号吗？

不会。当前首次版本成为活跃版本，后续由服务端 Activate 显式切换；已有 ChatSession 的 ToolSet 不会因此中途切版本。

### 为什么工具结果又要发给模型，而不是直接给浏览器？

因为模型需要基于真实结果继续推理和组织回答。网页也可以通过专门 API/SSE 显示计划、审批、产物等结构化状态，但这不能替代 role=tool 的模型上下文闭环。

### Tool 是不是只用于编程 Agent？

不是。查询业务数据、整理日程、生成报告、检索资料和发起审批都可以是工具能力。当前文件/Shell 工具只是本项目的一类能力，不能把整个产品的工具体系等同于代码执行。

### Tool 返回资料是否就可信？

工具运行在服务端，不代表其外部数据源可信。网页、文件、用户文档里的文字可能包含提示注入；它们是待处理数据，不是新的系统权限。服务端授权与沙箱不能依赖模型自觉遵守这些文字。

### Tool 是否等于当前缺失的完整任务编排引擎？

不是。当前循环、交互等待和进程内事件流适合这个阶段的学习与实现。跨进程恢复、多实例共享状态、完整执行历史、累计存储与搜索配额等仍需后续迭代；不要因工具数量增加就认为它们已经自动具备。

## 17. 阅读代码和动手学习路线

### 第一遍：先跑最短闭环

1. `backend/internal/tool/calculator.go`：一个没有持久化依赖的工具。
2. `backend/internal/provider/provider.go`：ToolDefinition、ToolCall、Message 的协议关系。
3. `backend/internal/service/chat.go`：找到 executeToolCalls 及其两个调用点。
4. 看下一轮请求的 messages，理解 role=assistant / role=tool 的配对。

### 第二遍：理解加载、安全与分层

1. `backend/internal/tool/builtin.go`、`backend/internal/httpapi/server.go`：创建与注册。
2. `backend/internal/tool/registry.go`、`tool.go`：快照、版本、Schema 与执行边界。
3. `backend/internal/tool/adapter.go`、`plan.go`：薄适配与泛型解码。
4. `backend/internal/service/plan.go`、`tool_scope.go`：业务规则与所有权。
5. `backend/internal/tool/interaction.go`、`backend/internal/service/interaction.go`：真实提问与审批。
6. `backend/internal/execution/`：文件与 Shell 的独立隔离边界。

### 第三遍：做练习并测试

按第 11 节新增 text_length，先跑不接模型的测试，再接入应用装配与对话。

已有测试可以按主题运行：

```bash
cd backend

# 注册、快照、安全校验与类型化参数映射。
go test ./internal/tool -count=1

# 流式/同步的模型 → 工具 → 模型循环。
go test ./internal/service -run 'TestChatServiceExecutesTool' -count=1

# 不带客户端幂等头时，仍可建立服务端身份并等待真实答复。
go test ./internal/service -run 'TestChatHumanToolWorksWithoutClientIdempotencyHeader' -count=1

# 一次性审批与业务校验。
go test ./internal/service -run 'TestApprovalRequiresRealUser|TestArtifactRoundTrip' -count=1
```

这些命令不需要调用真实付费模型。涉及模拟 HTTP 服务的其他测试需要允许监听本地临时端口；真实 Docker、PostgreSQL 集成测试要按对应文档配置依赖。

文档编写时已使用 Go overlay 将第 11 节的两份完整示例临时加入编译视图，并运行 TestTextLengthToolGuide 验证成功；这没有向业务源码目录写入练习工具，也没有把它注册到运行中的应用。

已有专题文档分工如下：

- [第一课：沙箱、文件与 Shell 的基础设施边界](./execution-01-sandbox-and-tools.md)。
- [第二课：Registry、12 个 MVP 工具、审批与持久化](./execution-02-tool-registry-and-mvp.md)。
- [第三课：统一适配器与类型化业务服务](./execution-03-typed-tools-and-services.md)。

最后，用这条链路检查自己是否理解：**创建工具对象 → 注册 → 为本轮用户过滤并固定快照 → 将定义发给模型 → 接收调用请求 → 服务端校验并执行 → 把结果放回上下文 → 模型继续回答。**
