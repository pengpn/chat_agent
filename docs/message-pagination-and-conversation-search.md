# 消息分页与对话搜索实现解析

本文说明项目如何为长对话和大量历史会话增加游标分页、向上加载与全文模糊搜索，并解释实现中的关键取舍。

## 1. 为什么不能继续一次性加载

旧接口会一次返回用户的全部对话，进入某个对话后又一次返回全部消息。数据量小时实现简单，但随着历史增长会同时放大：

- PostgreSQL 查询、排序和网络传输开销；
- Go JSON 序列化和浏览器 JSON 解析开销；
- Vue 响应式对象数量和 Markdown 渲染开销；
- 首屏等待时间和前端内存占用。

分页后的固定上界是：对话默认 30 条，消息默认 50 条，单页最大 100 条。数据库中即使存在一万条消息，浏览器首次仍只处理最近一页。

## 2. 为什么选择游标而不是 OFFSET

传统分页通常写成：

```sql
ORDER BY created_at DESC
LIMIT 50 OFFSET 9950;
```

OFFSET 越大，数据库需要跳过的记录越多；并且分页期间插入新数据时，后续页还可能重复或漏掉记录。

当前实现使用两个排序字段组成游标：

```text
(created_at, id)
```

时间是主要顺序，UUID 是时间相同时的确定性兜底。下一页消息查询等价于：

```sql
WHERE conversation_id = :conversation_id
  AND (created_at, id) < (:cursor_time, :cursor_id)
ORDER BY created_at DESC, id DESC
LIMIT :limit_plus_one;
```

对话列表使用相同思路，只是排序字段换成 `(updated_at, id)`。

## 3. 不透明游标的编码

`backend/internal/pagination/cursor.go` 将时间和 UUID 编码为 URL-safe Base64。前端只保存和回传字符串，不解析游标内容。

不透明游标的好处是：

- HTTP 协议不暴露数据库查询细节；
- 后续可以调整游标字段而不让前端直接依赖；
- 解码入口可以统一校验 Base64、时间和 UUID；
- 损坏游标统一返回 `400`，不会变成 PostgreSQL 类型错误。

## 4. 为什么查询 limit + 1

接口需要告诉前端是否还有下一页。若每页请求都额外执行 `COUNT(*)`，会增加一次数据库查询，而且总数对无限滚动并不重要。

Repository 实际查询 `limit + 1` 条：

- 返回数量小于等于 `limit`：已经到最后一页；
- 返回数量等于 `limit + 1`：还有下一页；
- 多出的最后一条只用于判断 `hasMore`，不会返回给前端；
- `nextCursor` 取实际返回页的最后一条记录。

该逻辑统一封装在 `pagination.BuildPage`。

## 5. 消息为什么数据库倒序、前端正序

用户进入对话时最关心最新消息，因此数据库必须从最新记录开始查，才能快速取得最近一页。但聊天界面阅读顺序仍然是从旧到新。

处理流程如下：

```text
PostgreSQL：最新 → 最早
        ↓ 返回一页
前端排序：最早 → 最新
        ↓
渲染聊天记录
```

加载更早页面后，`mergeMessages` 按消息 ID 去重，再按 `(createdAt, id)` 正序排列。ID 去重可以容忍分页期间新增消息或重试请求。

## 6. 向上加载时如何避免页面跳动

如果直接把旧消息插到数组前面，消息容器的 `scrollHeight` 会增大，而原来的 `scrollTop` 不变，用户看到的内容就会突然向下跳。

`ChatPanel` 在加载前记录：

```text
previousHeight
previousTop
```

Vue 完成 DOM 更新后再设置：

```text
newScrollTop = previousTop + newScrollHeight - previousHeight
```

这样新增内容占用的高度会被补偿，加载前位于视口中的文字仍然停留在原位置。向上滚动到 80px 范围会自动触发加载，同时保留一个可键盘操作的“加载更早消息”按钮作为明确入口和无障碍兜底。

## 7. 分支对话遇到分页边界怎么办

消息编辑和回答重新生成使消息结构成为树，而最近一页可能不包含树的根节点。旧渲染逻辑总是从根节点开始遍历，因此祖先不在当前页时会显示空白。

`frontend/src/lib/message-tree.ts` 会寻找“父节点不在当前已加载集合中的活动消息”，把它当作当前页的临时边界，再沿活动子节点继续渲染。加载更早页面后，真正的祖先进入集合，边界会自然向前扩展。

同一父节点下的非活动回答仍保存在 `versions` 中，用户可以切换当前已加载的回答版本。

## 8. 对话搜索如何保证用户隔离

搜索同时匹配：

- `conversations.title`；
- 当前对话任意 `messages.content`。

安全边界不依赖前端传入用户 ID。Repository 从认证上下文取得用户 ID，并将条件放进同一个 SQL：

```sql
WHERE conversations.user_id = :current_user_id
  AND (
    conversations.title ILIKE :pattern
    OR EXISTS (
      SELECT 1
      FROM messages
      WHERE messages.conversation_id = conversations.id
        AND messages.content ILIKE :pattern
    )
  )
```

`user_id` 条件位于搜索条件之外，因此标题或消息命中都不能越过用户边界。`%`、`_` 和反斜杠会被转义，用户输入按普通文本搜索，不会意外扩大为通配模式。

前端输入采用 300ms debounce，避免每输入一个字符都立即发起请求；空结果会展示可理解的提示，而不是留下空白侧栏。

## 9. 数据库索引

迁移会启用 PostgreSQL `pg_trgm`，并建立四个主要索引：

```sql
conversations (user_id, updated_at DESC, id DESC)
messages      (conversation_id, created_at DESC, id DESC)
GIN (conversations.title gin_trgm_ops)
GIN (messages.content gin_trgm_ops)
```

前两个 B-tree 索引对应用户对话分页和会话消息分页；两个 trigram GIN 索引用于包含式 `ILIKE '%关键词%'` 搜索。

## 10. API 契约

### 对话列表与搜索

```http
GET /api/v1/conversations?q=Agent&limit=30&cursor=<opaque_cursor>
```

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "conversations": [],
    "nextCursor": "...",
    "hasMore": true
  }
}
```

### 消息分页

```http
GET /api/v1/conversations/:conversationId/messages?limit=50&cursor=<opaque_cursor>
```

消息数组按最新到最早返回。前端使用前会转成时间正序。

`limit` 必须在 `1..100` 内；省略时使用各接口默认值。游标为空表示第一页。

## 11. 测试与性能验证

自动测试覆盖：

- 游标编码、解码和非法输入；
- `limit + 1` 的下一页判断；
- 前端旧消息合并、排序和去重；
- 分页边界缺少祖先时的活动分支渲染；
- 回答版本分组。

PostgreSQL 集成测试 `TestPostgresPaginationAndSearchWithTenThousandMessages` 会：

1. 建立两个隔离用户；
2. 为主测试会话插入一万条消息；
3. 验证第一页查询耗时和固定返回数量；
4. 验证连续两页没有重复消息；
5. 验证能通过消息正文找到自己的对话；
6. 验证无法搜到另一个用户的私有消息；
7. 测试完成后删除临时用户并级联清理数据。

运行方式：

```bash
cd backend
TEST_DATABASE_URL='postgres://...' go test ./internal/repository \
  -run TestPostgresPaginationAndSearchWithTenThousandMessages -count=1 -v

cd ../frontend
npm test
npm run build
```

本地 Docker PostgreSQL 的一万条消息集成测试总耗时约 0.7 秒（包含临时数据创建、查询断言和清理）；前端首次渲染数量始终受单页上限约束。该数字只用于本地回归，不代表生产环境 SLA。

## 12. 后续可继续优化

- 搜索结果增加命中片段和关键词高亮；
- 使用 PostgreSQL `tsvector` 做语言分词、相关性排序；
- 消息数量进一步增大时引入前端虚拟列表；
- 通过版本化 Migration 管理索引创建，避免生产启动时执行 DDL；
- 为搜索增加独立超时、查询指标和慢查询采样。
