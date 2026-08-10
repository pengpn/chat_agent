import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDisplayMessages } from './message-tree.ts'

function message(id: string, role: 'user' | 'assistant', createdAt: string, parentMessageId?: string, isActiveChild = true) {
  return {
    id, role, createdAt, parentMessageId, isActiveChild,
    conversationId: 'c1', content: id, status: 'complete' as const,
  }
}

test('最近一页缺少祖先节点时仍能渲染当前活动分支', () => {
  const items = [
    message('a2', 'assistant', '2026-08-10T00:00:02Z', 'user-outside-page'),
    message('u3', 'user', '2026-08-10T00:00:03Z', 'a2'),
    message('a3', 'assistant', '2026-08-10T00:00:04Z', 'u3'),
  ]
  assert.deepEqual(buildDisplayMessages(items).map((entry) => entry.message.id), ['a2', 'u3', 'a3'])
})

test('同一父节点的非活动回答作为版本保留但不进入当前分支', () => {
  const items = [
    message('u1', 'user', '2026-08-10T00:00:01Z'),
    message('a-old', 'assistant', '2026-08-10T00:00:02Z', 'u1', false),
    message('a-new', 'assistant', '2026-08-10T00:00:03Z', 'u1', true),
  ]
  const entries = buildDisplayMessages(items)
  assert.deepEqual(entries.map((entry) => entry.message.id), ['u1', 'a-new'])
  assert.deepEqual(entries[1].versions.map((item) => item.id), ['a-old', 'a-new'])
})
