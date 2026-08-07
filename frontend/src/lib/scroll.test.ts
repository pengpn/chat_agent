import assert from 'node:assert/strict'
import test from 'node:test'

import { isNearScrollBottom } from './scroll.ts'

test('位于底部时继续跟随流式输出', () => {
  assert.equal(isNearScrollBottom({ scrollTop: 600, clientHeight: 400, scrollHeight: 1000 }), true)
})

test('距离底部不超过阈值时仍视为跟随状态', () => {
  assert.equal(isNearScrollBottom({ scrollTop: 530, clientHeight: 400, scrollHeight: 1000 }, 80), true)
})

test('用户向上滚动后停止自动跟随', () => {
  assert.equal(isNearScrollBottom({ scrollTop: 300, clientHeight: 400, scrollHeight: 1000 }, 80), false)
})
