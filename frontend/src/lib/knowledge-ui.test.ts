import assert from 'node:assert/strict'
import test from 'node:test'

import {
  annotateCitationHtml,
  citationLocation,
  citationSourceAvailable,
  describeKnowledgeScope,
  documentIsProcessing,
  documentStatusLabel,
} from './knowledge-ui.ts'

test('文档状态使用用户能理解的处理进度', () => {
  assert.equal(documentStatusLabel('extracting'), '正在读取内容')
  assert.equal(documentStatusLabel('ready'), '可以引用')
  assert.equal(documentStatusLabel('failed'), '处理失败')
  assert.equal(documentIsProcessing('chunking'), true)
  assert.equal(documentIsProcessing('ready'), false)
})

test('检索范围会列出当前选中的知识库', () => {
  assert.match(describeKnowledgeScope([]), /未选择知识库/)
  assert.equal(describeKnowledgeScope(['制度库']), '将在「制度库」中查找依据')
  assert.equal(describeKnowledgeScope(['制度库', '产品手册']), '将在「制度库」、「产品手册」中查找依据')
})

test('引用位置保留页码和章节，删除后的来源不能再下载', () => {
  assert.equal(citationLocation({ pageNumber: 3, sectionTitle: '退款时限' }), '第 3 页 · 退款时限')
  assert.equal(citationLocation({ sectionTitle: '成员限制' }), '成员限制')
  assert.equal(citationSourceAvailable({ knowledgeBaseId: 'base', documentId: 'doc' }), true)
  assert.equal(citationSourceAvailable({ documentId: 'doc' }), false)
})

test('只把回答里真实存在的引用编号变成按钮，代码块保持原文', () => {
  const html = '<p>最多 100 人[1]，见附录[9]。</p><pre><code>items[1]</code></pre>'
  const annotated = annotateCitationHtml(html, [1])
  assert.match(annotated, /data-citation="1"/)
  assert.match(annotated, /查看来源 1/)
  assert.match(annotated, /附录\[9\]/)
  assert.match(annotated, /<code>items\[1\]<\/code>/)
})
