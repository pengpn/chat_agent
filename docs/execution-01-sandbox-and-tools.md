# 执行工具第一阶段：会话沙箱、文件操作与 Shell

RAG 暂时放下，已有功能保留。这一节从「模型只能说」进入「模型可以请求服务端执行动作」：实现图里的六个 P0 工具；交互式进程和差异查看放在下一节。

本节选择 A 方案：每个用户、每个会话拥有独立工作区，不是让模型直接操作当前后端项目，也不是允许网页传入任意本地目录。

## 1. 实现了哪些工具

| 逻辑名称 | 模型调用名 | 本节行为 |
| --- | --- | --- |
| `shell.exec` | `shell_exec` | 在受限 Docker 容器中同步执行 Shell，返回输出和退出码 |
| `file.list` | `file_list` | 分页列出工作区目录 |
| `file.read` | `file_read` | 按字节分段读取 UTF-8 文本，返回完整文件 SHA-256 |
| `file.write` | `file_write` | 创建或带版本条件覆盖文本，自动创建父目录 |
| `file.apply_patch` | `file_apply_patch` | 按顺序精确替换一个文件，全部校验成功后写入 |
| `file.search` | `file_search` | 递归搜索文本，返回路径、行号及短片段 |

供应商协议使用下划线名称，文档保留图中按域分组的逻辑名称。这些名字是服务端白名单，不允许模型自己定义新的可执行函数。

第一课完成时尚未加入交互式进程、差异查看和人工审批。随后[第二课](./execution-02-tool-registry-and-mvp.md)已实现统一 Registry、Shell 逐次审批和其余 MVP 工具；`shell.write_stdin`、`file.diff`、前端文件浏览器及附件自动导入仍留在 TODO。

## 2. 它接在现有链路的什么位置

```text
ChatService → LLMGateway → 模型返回 tool_calls
                              │
                              ▼
                       Tool Registry
                  权限 / Schema / 超时 / 审计
                              │
                    服务端注入用户和会话 Scope
                              │
                              ▼
                      Execution Gateway
                  会话串行化 / 容量 / 路径边界
                       │              │
                       ▼              ▼
                  文件快照操作     Docker Runner
                       └──────┬───────┘
                              ▼
                   工具 JSON 结果 → 下一轮模型
```

`LLMGateway` 仍只负责模型调用。执行网关负责文件和进程，不把执行 Shell 的职责混进模型网关。

模型输出只是「调用建议」。只有 Registry 授权并通过校验后才执行；结果以原有 `role=tool` 消息回传，继续生成最终回答。同步 Chat 和流式 ChatStream 共用这条工具执行路径。

## 3. 为什么身份不能放在工具参数里

模型参数只包含 `path`、`content`、`command` 等业务输入，不包含 `userID`、`conversationID`、镜像、宿主目录、网络或 Docker 参数。

`ChatService.executeToolCalls` 使用已经通过鉴权、会话所有权检查的 `session.UserID` 和 `session.UserMessage.ConversationID`，调用 `ExecuteForConversation`。后者通过 Context 写入 `execution.Scope`。

执行网关计算 `SHA256(userID + 分隔符 + conversationID)` 作为工作区目录键。因此：

- 不同用户即使传相同文件名，也不会共享文件。
- 同一个用户不同会话的文件隔离。
- 同一会话的消息分支共享同一工作区，不自动创建文件分支。
- 模型伪造 `userID` 等字段，会被 JSON Schema 和严格解码拒绝。
- 没有服务端 Scope 的文件/Shell 调用直接拒绝。

哈希不是鉴权机制；真正的所有权检查在对话业务入口，哈希只负责稳定命名。

## 4. 文件操作的四道边界

### 4.1 相对路径与 os.Root

路径必须是工作区内的相对路径，拒绝绝对路径、`..`、反斜杠、NUL 和过长路径。文件路径不能是 `.`；列表和搜索可以使用 `.` 表示根目录。

宿主文件访问使用 Go 1.24 引入的 `os.Root`。它针对路径越界和符号链接逃逸提供文件访问约束，不是只用字符串前缀判断；但它也不是进程沙箱，更不阻止所有硬链接、设备和挂载行为，所以还需要拒绝链接/特殊文件以及独立的容器边界。[Go 官方说明](https://go.dev/blog/osroot)

项目 `go.mod` 和 README 最低版本已同步为 Go 1.24，本地 `.tool-versions` 已是 1.24.5。正式部署应使用受支持且包含安全修复的 Go 版本，而不是把这个最低版本当作生产版本推荐。

### 4.2 有界快照

文件操作读取当前工作区快照，限制单文件大小、所有普通文件总大小，以及文件和目录总数。读取只支持无 NUL 的 UTF-8 文本；Shell 可产生二进制普通文件，但不能用 `file_read` 把它们当文本读取。

同一会话的文件/Shell 操作串行化，服务整体还有执行并发上限。等待锁或执行名额时也响应 Context 取消。固定数量的分片锁避免历史会话越多、锁对象越多。

这不是高性能大文件文件系统：学习版会扫描和重建有界快照。默认 16 MiB 工作区、1 MiB 单文件、512 个文件/目录，适合小规模代码和文档任务。

### 4.3 先读再改：SHA-256 比较并写入

创建新文件不需要旧哈希；覆盖已有文件必须提供 `file_read` 返回的最新哈希：

```json
{"path":"notes/hello.txt","content":"你好，Agent"}
```

```json
{"path":"notes/hello.txt"}
```

读取返回 `sha256` 后，再覆盖：

```json
{"path":"notes/hello.txt","content":"新的完整内容","expectedSha256":"读取返回的64位哈希"}
```

另一个调用已经改过文件时，旧哈希会失效，必须重新读取。哈希检查和写入位于同一会话锁内，不会出现两个调用都拿旧版本成功覆盖的情况。

### 4.4 增量修改不等于 Unified Diff

本节选择容易学习、容易校验的精确替换契约：

```json
{
  "path": "notes/hello.txt",
  "expectedSha256": "读取返回的64位哈希",
  "edits": [
    {"oldText": "你好，Agent", "newText": "你好，工具世界"}
  ]
}
```

每个 `oldText` 必须非空，且在上一步替换后的文本中恰好出现一次。不匹配或匹配多次都会拒绝，不能猜修改位置。最多 20 项替换，任意一项失败都不写入。

修改先写到服务端暂存目录，完整成功后再切换工作区目录；普通写入错误会保留原工作区。但这不是数据库事务，也没有实现断电恢复日志：进程崩溃时可能遗留 `stage-*` 或 `backup-*`，后续需要清理和恢复机制。

## 5. Shell 为什么不能直接 exec.Command("sh", "-c", command)

那会以后端身份执行模型命令，可能读取应用密钥、修改源码、访问数据库网络。这里宿主机仅执行固定结构的 Docker CLI；模型命令始终作为容器内 `/bin/sh -lc` 的一个参数，不经过宿主 Shell。

固定容器限制包括：禁网、非 root UID 65532、只读根文件系统、删除全部 Linux capabilities、禁止提权、限制 CPU/内存/进程数、不自动拉取镜像、不挂载宿主目录或 Docker Socket。[Docker 运行参数](https://docs.docker.com/reference/cli/docker/container/run/)

`/workspace` 和 `/tmp` 使用容量受限的 tmpfs，而不是可无限写满宿主磁盘的绑定挂载。工作区内存盘设置为文件总容量的两倍，为目录和文件系统开销留空间；实际占用仍受容器总内存限制。

一次 Shell 的过程：

1. 读取并检查当前会话快照。
2. 创建受限容器，通过固定 `tar` 命令把快照送入内存盘。
3. 执行模型命令，合并捕获 stdout/stderr，超出输出上限继续消费但丢弃。
4. 暂停遗留的同 UID 模型子进程，避免后台任务在导出时继续修改文件；不支持保留交互进程。
5. 导出 tar 到 Go，拒绝符号链接、硬链接、设备、重复路径、越界路径和超大内容。
6. 全部检查通过且请求未取消时，同步新的会话快照。
7. 使用独立的清理 Context 强制删除容器。

为什么不用 `docker cp` 直接解包到宿主目录？既要自己检查不可信归档，也要保持只读根文件系统。真实测试确认 Docker 的归档复制接口会拒绝写入只读根文件系统，因此改用容器内固定 tar 命令，不为方便复制而放宽隔离参数。

命令非零退出是正常工具结果，例如 `exitCode=7`，不是模型请求失败；已产生的合法文件变更仍会同步。超时、取消、执行基础设施故障或快照校验失败时，不同步容器半成品。

停止生成不会回滚此前已经完成的文件工具操作。容器清理失败会记录 `sandbox cleanup failed`，不能把“杀掉 Docker 客户端”当成容器一定已停止。服务崩溃的遗留容器回收还没有实现。

Shell 不自动重试；模型再次请求同一条命令，仍可能再次执行。文件哈希用于防止过期覆盖，不等同于覆盖全部工具副作用的幂等存储；入口仍沿用原有业务消息幂等机制。

## 6. 如何启用和试用

配置已经同步到 `.env` 和 `.env.example`，默认不注册执行工具：

```dotenv
EXECUTION_ENABLED=false
EXECUTION_ADMIN_ONLY=true
EXECUTION_WORKSPACE_ROOT=./data/tool-workspaces
EXECUTION_DOCKER_BINARY=docker
EXECUTION_IMAGE=chat-agent-sandbox:local
EXECUTION_TIMEOUT=20s
EXECUTION_MEMORY_MB=256
EXECUTION_CPUS=1
EXECUTION_PIDS=64
EXECUTION_MAX_CONCURRENT=2
EXECUTION_MAX_WORKSPACE_BYTES=16777216
EXECUTION_MAX_FILE_BYTES=1048576
EXECUTION_MAX_ENTRIES=512
EXECUTION_MAX_OUTPUT_BYTES=4096
```

先在项目根目录构建专用镜像：

```bash
docker build -f backend/sandbox/Dockerfile -t chat-agent-sandbox:local backend/sandbox
```

镜像构建需要访问镜像仓库和 Alpine 软件源；模型执行期间仍禁网。Docker CLI 必须连接本机 Docker Engine：当前入口不面向远程执行节点部署，也不建议把宿主 Docker Socket 挂入公网 API 容器。

构建成功后设置 `EXECUTION_ENABLED=true` 并重启后端。默认只有管理员可以使用；确认账户角色。`EXECUTION_ADMIN_ONLY=false` 才允许普通用户使用各自工作区，本节不建议在公网开放。

`TOOL_ADMIN_ONLY` 仍可额外收紧某个工具，不会覆盖独立的管理员开关。工具定义在发给模型之前按权限过滤，实际执行时再检查一次。

可以向支持 Tool Calling 的模型发送：

> 在你的沙箱工作区创建 notes/hello.txt，写入“你好，Agent”，读取确认后把 Agent 改成“工具世界”，最后告诉我文件内容。

或者：

> 在沙箱中执行 pwd 和 id，创建一个简单的 Python 脚本并运行，告诉我退出码。

模型是否选择调用工具仍由模型决定；这些能力不是网页本地终端。当前没有文件浏览器，只能通过工具结果读取信息。

读取使用 `offset/limit` 字节分页，后续读取使用 `nextOffset`；同一页的 `sha256` 是整个文件的哈希。列表的分页单位则是目录项。搜索默认字面量，可用 `regex=true` 开启正则；被截断时应缩小范围，不把截断结果当成完整列表。

`MAX_TOOL_ITERATIONS` 默认四轮，复杂任务可能需要拆开。工具返回的 JSON、Schema、文件文本也会消耗模型上下文 Token。

## 7. 配置限制与数据存放

`EXECUTION_TIMEOUT` 约束整个 Shell 准备、执行和导出，现有 `TOOL_EXECUTION_TIMEOUT` 还会施加外层超时，取先到者。容器删除单独最多等待五秒。

工具输出必须是完整 JSON，不能经过 Registry 的通用字符串截断。配置加载会要求 `TOOL_RESULT_MAX_BYTES >= EXECUTION_MAX_OUTPUT_BYTES × 6 + 8192`，为 JSON 转义、路径和元数据预留空间；工具适配器还会做最后大小检查。

默认从 backend 启动时，文件存放在 `backend/data/tool-workspaces/`。目录已加入 Git 忽略，文件以本地明文快照保存，不在 PostgreSQL，也不自动进入 MinIO 或 RAG。

当前删除会话/注销账号尚未自动删除这些快照；不能沿用此前“消息和附件已删除”就推断工作区也删除。管理员应将其视为开发测试数据，后续 E2 要补生命周期、保留策略与遗留资源回收，再考虑普通用户开放。

## 8. 代码阅读顺序和验证

以下路径相对于 `backend/`：

1. `internal/tool/file.go`、`shell.go`、`adapter.go`：具体工具定义、严格参数解码和结果编码；Schema 统一由 Registry 编译与校验，拆分说明见[第三课](./execution-03-typed-tools-and-services.md)。
2. `internal/tool/tool.go`：权限过滤、会话 Scope、执行时再次授权及审计。
3. `internal/service/chat.go`：同步/流式循环如何传入服务端会话身份。
4. `internal/execution/gateway.go`：独立工作区、全局并发和会话锁。
5. `internal/execution/files.go`、`workspace.go`：版本条件、完整暂存和容量限制。
6. `internal/execution/docker.go`、`archive.go`：容器参数、取消清理、安全归档。
7. `internal/config/execution.go`、`cmd/api/main.go`：配置和依赖组装。

普通后端测试不会启动 Docker，也不调用付费模型：

```bash
cd backend
go test ./...
go test -race ./internal/execution ./internal/tool ./internal/service
```

真实容器测试需要显式开启：

```bash
go test ./internal/execution -run TestDockerSandboxIsolationSyncAndCancellation \
  -v -timeout 60s -args -docker-sandbox
```

可以用 `-sandbox-image 已缓存镜像名` 检查运行时链路，但替代镜像必须提供 `/bin/sh`、`tar`、`kill`、`sleep` 和测试所用基础命令；这不等于验证专用镜像已构建成功。

本轮专用镜像因 Docker Hub 访问超时未构建成功。真实执行验证使用本机已缓存的官方 `golang:1.25.6-alpine3.23`，覆盖空工作区、非 root、只读根目录、无网络路由、文件往返、非零退出码、输出截断、后台进程冻结、非法链接导出和取消清理。没有调用真实 LLM。

下一节学习 `shell.write_stdin` 和 `file.diff`：届时才引入跨工具调用的进程 Session、增量输出游标和差异复核。Docker 共用宿主内核，本节是默认关闭的本地学习边界，不是对恶意公网多租户的完整生产隔离保证。
