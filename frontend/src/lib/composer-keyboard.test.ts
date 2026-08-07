import assert from 'node:assert/strict'
import test from 'node:test'

import { shouldSendComposerMessage } from './composer-keyboard.ts'

test('中文输入法组合输入期间按 Enter 不发送消息', () => {
  assert.equal(shouldSendComposerMessage({
    key: 'Enter',
    shiftKey: false,
    isComposing: true,
    keyCode: 229,
  }), false)
})

test('浏览器只通过 keyCode=229 标记输入法事件时不发送消息', () => {
  assert.equal(shouldSendComposerMessage({
    key: 'Enter',
    shiftKey: false,
    isComposing: false,
    keyCode: 229,
  }), false)
})

test('非组合输入状态下 Enter 正常发送消息', () => {
  assert.equal(shouldSendComposerMessage({
    key: 'Enter',
    shiftKey: false,
    isComposing: false,
    keyCode: 13,
  }), true)
})

test('Shift+Enter 只换行，不发送消息', () => {
  assert.equal(shouldSendComposerMessage({
    key: 'Enter',
    shiftKey: true,
    isComposing: false,
    keyCode: 13,
  }), false)
})
