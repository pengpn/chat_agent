# 编辑消息与对话分支实现解析

本文记录“编辑历史消息”在本项目中的完整实现。重点不是编辑框本身，而是如何在不破坏旧对话的前提下，把线性消息列表升级为可切换的对话树，并确保模型只读取用户当前选择的分支。

## 1. 为什么不能直接更新原消息

如果对历史用户消息直接执行 `UPDATE messages SET content = ...`，它后面的 assistant 回答和后续问答就失去了原始问题，已有上下文也变得前后矛盾。更严重的是，旧回答产生过的 Token、成本和审计记录无法再解释。

因此本项目把“编辑”定义为：

1. 保留原用户消息和它下面的完整子树；
2. 在同一个父节点下创建一条新的用户消息；
3. 在新用户消息下创建新的 assistant 消息并调用模型；
4. 把新用户消息设为该层当前选中的子节点；
5. 用户仍可切回旧用户消息，从而恢复旧分支。

这是一种 copy-on-write 思路：读历史时共享不变的祖先，发生编辑时只复制分叉点之后的新节点。

## 2. 消息树的数据结构

`messages` 表新增三个字段：

| 字段 | 含义 |
| --- | --- |
| `parent_message_id` | 当前消息的直接父消息；根消息为 `NULL` |
| `is_active_child` | 在同角色、同父节点的兄弟消息中，当前是否被选中 |
| `edited_from` | 新用户消息由哪条旧用户消息编辑而来，仅用于追踪来源 |

已有的回答版本字段继续保留：

- `reply_to_message_id`：assistant 回答对应的用户消息；
- `response_version`：同一个问题下的回答版本号；
- `is_active_response`：当前回答版本，与 assistant 的 `is_active_child` 同步；
- `regenerated_from`：重新生成来源。

一段存在编辑和重新生成的对话可以表示为：

```text
U1 原问题 [inactive]
├── A1 原回答 [active in old subtree]
│   └── U2 原后续问题
│       └── A2 原后续回答
└── （U1 的后代只属于旧分支）

U1' 编辑后的问题 [active, edited_from=U1]
├── A1' 第一次回答 [inactive]
└── A1'' 重新生成回答 [active]
```

注意：`U1` 和 `U1'` 实际是同一父节点下的兄弟节点，而不是父子关系。图中分开写只是为了突出两个可选分支。

## 3. 当前分支如何确定

服务端和前端使用相同规则：从 `parent_message_id IS NULL` 的根兄弟组开始，每一层选择 `is_active_child=true` 的消息，再以该消息 ID 查找下一层子节点，直到没有激活子节点为止。

```text
根兄弟组 → active user → active assistant → active user → active assistant → ...
```

数据库仍返回整棵树，因为前端切换版本需要看到兄弟节点；但展示区和模型上下文只沿 active path 读取。实现位置：

- 后端：`internal/service/context.go` 的 `selectedBranch`；
- 前端：`ChatPanel.vue` 的 `displayMessages` 计算属性。

代码还保留了旧数据兼容路径：如果消息完全没有树字段，则按原来的 `reply_to_message_id + is_active_response` 规则展示和构建历史。

## 4. 编辑消息的请求链路

接口：

```http
POST /api/v1/conversations/:conversationID/messages/:messageID/edit
Authorization: Bearer <token>
Idempotency-Key: <uuid>
Content-Type: application/json

{
  "content": "修改后的问题",
  "provider": "deepseek",
  "model": "deepseek-chat",
  "temperature": 0.7
}
```

该接口使用 SSE 返回，事件格式与普通发送、重新生成一致。

```mermaid
sequenceDiagram
    participant UI as Vue 前端
    participant H as Gin Handler
    participant S as ChatService
    participant R as Repository
    participant G as LLMGateway

    UI->>H: POST edit + Idempotency-Key
    H->>S: PrepareEdit
    S->>S: 校验目标必须是 complete user 消息
    S->>G: Begin（整次编辑操作幂等）
    S->>R: CreateEditedTurn
    R->>R: 锁定 conversation
    R->>R: 激活目标祖先路径并创建新 user + assistant
    R->>R: 失效旧摘要
    S->>S: 仅使用目标消息之前的祖先构建上下文
    H-->>UI: metadata（新 user / assistant）
    H->>S: Stream
    S->>G: ChatStream
    H-->>UI: think / delta / done
```

`PrepareEdit` 与普通发送共用模型、工具、思考、结构化输出、Token 预算和流式终态逻辑，但它有两项特殊行为：

- 幂等作用域加入 `edit + targetMessageID`，一次业务编辑不会因为内部重试重复建分支；
- 上下文使用 `ancestryBefore` 获取目标用户消息之前的精确祖先链，不读取其他兄弟分支，也不读取目标消息后面的旧回答。

## 5. 为什么切换深层节点要激活整条祖先路径

只把某条深层消息设为 active 还不够。如果它的父消息属于一个未激活的旧分支，从根节点沿 active child 遍历时永远到不了它。

Repository 的 `activateMessagePath` 会从目标节点不断向上查父节点，并在每一层：

1. 关闭同角色、同父节点的兄弟节点；
2. 激活当前节点；
3. assistant 节点同时同步 `is_active_response`；
4. 继续处理父节点，直到根节点。

因此无论前端逐层切换，还是未来通过深链接直接选择历史节点，当前分支都能保持一致。

切换接口为：

```http
PATCH /api/v1/conversations/:conversationID/messages/:messageID/activate
```

它既可切换用户问题分支，也可切换 assistant 回答版本。

## 6. 事务与并发编辑

编辑和分支切换都先对 `conversations` 行执行 `SELECT ... FOR UPDATE`。同一会话内的并发操作会串行进入临界区，事务随后完成：

- 幂等键检查；
- 目标消息和权限检查；
- 兄弟节点 active 状态更新；
- 新 user 与 assistant 同时创建；
- 会话摘要失效；
- 会话更新时间更新。

并发编辑同一问题时，两条新分支都会保留，但最后获得会话锁的事务成为当前 active 分支。这样不会丢历史，也不会出现两个 active 根节点。

目前这是一种“最后完成的分支选择生效”策略。如果未来需要多人实时协作，可再增加乐观锁版本号，让旧页面提交时返回冲突并提示用户确认。

## 7. 为什么切换分支会清空滚动摘要

滚动摘要是基于当时选中分支生成的。切换分支后继续复用旧摘要，可能把另一个分支的内容带进模型请求，造成上下文串线。

因此编辑、重新生成和主动切换分支都会清空 `conversations.summary` 及摘要断点。下一次上下文超预算时，再针对新分支生成摘要。历史摘要版本仍保留在审计表中，但不会自动成为新分支的活跃摘要。

这是正确性优先的实现。后续若摘要成本成为瓶颈，可以按“分支叶子 ID + 摘要断点”缓存不同分支的摘要，而不是让所有分支共享一个摘要槽位。

## 8. 前端交互

用户消息新增“编辑”入口：

- 点击后原地显示文本框，不使用浏览器 `prompt/alert`；
- 自动聚焦并保留原文；
- `Ctrl/Cmd + Enter` 保存并生成，`Esc` 取消；
- 空内容和未修改内容不能提交；
- 生成期间禁用编辑与分支切换，避免重复调用；
- 用户消息的版本控件称为“问题分支”，assistant 的版本控件称为“回答版本”，并提供对应的无障碍标签。

收到 SSE `metadata` 后，前端立即插入新 user 与 assistant 节点并激活新路径；后续 `delta`、`think` 和终态事件仍复用原有流处理逻辑。

## 9. 迁移和删除

项目当前使用 GORM `AutoMigrate`。首次增加 `parent_message_id` 时会执行一次兼容回填：

- assistant 的父节点取原 `reply_to_message_id`；
- 用户消息的父节点取同会话中创建时间早于它的最近 assistant；
- 原 active assistant 同步为 active child；
- 旧用户消息标记为 active child。

回填完成状态记录在 `app_schema_migrations`。不能只检查新列是否存在：`AutoMigrate` 可能已经成功增加列，数据回填却在随后失败；此时重启必须再次执行回填。迁移会在同一个数据库事务中获得 PostgreSQL advisory lock、执行回填并写入版本标记，失败时不会留下“已完成”标记，多实例同时启动也不会重复迁移。

这是针对当前线性历史的升级策略。生产环境应把它改为有版本号、可审计和可回滚的 migration，而不是长期依赖启动时自动迁移。

`messages.conversation_id` 原有外键使用级联删除，因此删除会话时整棵消息树会一起删除，不需要递归逐节点删除。

## 10. 测试覆盖

本次增加或扩展的测试包括：

- 编辑产生用户兄弟节点和新的 assistant 子节点；
- 编辑请求只向模型传入目标之前的祖先，不混入其他分支；
- 当前上下文只沿 active path 读取；
- 并发编辑保留所有分支，并保证同层只有一个 active child；
- PostgreSQL 集成测试验证真实事务串行和删除会话后的级联清理。

本地运行单元测试：

```bash
cd backend
go test ./...
go test -race ./internal/service
```

设置独立测试数据库后运行 PostgreSQL 集成测试：

```bash
TEST_DATABASE_URL='postgres://user:password@localhost:5432/chat_agent_test?sslmode=disable' \
go test ./internal/repository -run TestPostgresConcurrentEditsAndConversationCascade
```

请勿把开发库或生产库配置为 `TEST_DATABASE_URL`；集成测试会创建并删除测试数据，并可能执行 schema migration。

## 11. 当前边界与下一步

- 消息接口目前仍一次返回整棵树；长对话需要在 TODO 第 5 项引入游标分页，并设计分支节点的按需加载。
- active 状态同时保存在数据库中，适合单用户会话；分享会话或团队协作时，应明确它是全局选择还是每个观看者的个人选择。
- 当前编辑失败会保留新建的 error assistant 节点，便于审计和重试；产品上可增加“删除失败分支”或“再次生成”。
- 当前使用会话行锁保证一致性；超高频协作场景可考虑乐观锁或专门的分支选择表。

理解这一功能的关键是：编辑不是字符串更新，而是一次不可变历史上的分叉；`parent_message_id` 保存所有可能路径，`is_active_child` 只保存当前选择，模型上下文永远由当前选择路径推导。
