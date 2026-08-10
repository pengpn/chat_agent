import assert from 'node:assert/strict'
import test from 'node:test'

import { mergeMessages } from './list-pagination.ts'

function message(id: string, createdAt: string) {
  return { id, createdAt, conversationId: 'c1', role: 'user' as const, content: id, status: 'complete' as const }
}

test('加载更早消息后按时间正序合并并去重', () => {
  const current = [message('m3', '2026-08-10T00:00:03Z'), message('m4', '2026-08-10T00:00:04Z')]
  const older = [message('m2', '2026-08-10T00:00:02Z'), message('m3', '2026-08-10T00:00:03Z')]
  assert.deepEqual(mergeMessages(older, current).map((item) => item.id), ['m2', 'm3', 'm4'])
})
