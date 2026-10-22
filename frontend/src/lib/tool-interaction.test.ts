import assert from 'node:assert/strict'
import test from 'node:test'
import { formatInteractionCountdown, remainingInteractionSeconds } from './tool-interaction.ts'

const start = Date.parse('2026-10-22T04:00:00Z')
const expiresAt = new Date(start + 90_000).toISOString()

test('审批时间按绝对截止时间计算，不因后台计时器跳帧累积误差', () => {
  assert.equal(remainingInteractionSeconds(expiresAt, start), 90)
  assert.equal(remainingInteractionSeconds(expiresAt, start + 10_000), 80)
  assert.equal(remainingInteractionSeconds(expiresAt, start + 68_300), 22)
  assert.equal(remainingInteractionSeconds(expiresAt, start + 89_999), 1)
  assert.equal(remainingInteractionSeconds(expiresAt, start + 90_000), 0)
  assert.equal(remainingInteractionSeconds(expiresAt, start + 100_000), 0)
})

test('倒计时在分钟与秒数边界保持可读', () => {
  assert.equal(formatInteractionCountdown(90), '剩余 1 分 30 秒')
  assert.equal(formatInteractionCountdown(60), '剩余 1 分 00 秒')
  assert.equal(formatInteractionCountdown(59), '剩余 59 秒')
  assert.equal(formatInteractionCountdown(1), '剩余 1 秒')
  assert.equal(formatInteractionCountdown(0), '等待已到期')
})

test('无效截止时间不可产生 NaN 或无限等待', () => {
  assert.equal(remainingInteractionSeconds('invalid', start), 0)
  assert.equal(remainingInteractionSeconds(expiresAt, Number.NaN), 0)
  assert.equal(formatInteractionCountdown(Number.NaN), '等待已到期')
})
