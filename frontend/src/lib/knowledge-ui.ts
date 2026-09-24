import type { KnowledgeDocumentStatus, MessageCitation } from '@/types'

export const MAX_SELECTED_KNOWLEDGE_BASES = 8

const STATUS_LABEL: Record<KnowledgeDocumentStatus, string> = {
  uploaded: '等待处理',
  extracting: '正在读取内容',
  chunking: '正在整理段落',
  embedding: '正在准备检索',
  ready: '可以引用',
  failed: '处理失败',
}

export function documentStatusLabel(status: string): string {
  return STATUS_LABEL[status as KnowledgeDocumentStatus] ?? '状态未知'
}

export function documentIsProcessing(status: string): boolean {
  return status === 'uploaded' || status === 'extracting' || status === 'chunking' || status === 'embedding'
}

export function describeKnowledgeScope(names: string[]): string {
  if (!names.length) return '未选择知识库，本次只根据对话回答'
  const quoted = names.map((name) => `「${name}」`).join('、')
  return `将在${quoted}中查找依据`
}

export function citationLocation(citation: Pick<MessageCitation, 'pageNumber' | 'sectionTitle'>): string {
  const parts: string[] = []
  if (citation.pageNumber) parts.push(`第 ${citation.pageNumber} 页`)
  if (citation.sectionTitle) parts.push(citation.sectionTitle)
  return parts.join(' · ')
}

export function citationSourceAvailable(citation: Pick<MessageCitation, 'knowledgeBaseId' | 'documentId'>): boolean {
  return Boolean(citation.knowledgeBaseId && citation.documentId)
}

export function annotateCitationHtml(html: string, ordinals: readonly number[]): string {
  const allowed = new Set(ordinals)
  if (!allowed.size) return html
  const parts = html.split(/(<pre\b[^>]*>[\s\S]*?<\/pre>|<code\b[^>]*>[\s\S]*?<\/code>)/gi)
  return parts.map((part, index) => (index % 2 === 1 ? part : replaceCitationMarkers(part, allowed))).join('')
}

function replaceCitationMarkers(text: string, allowed: Set<number>): string {
  return text.replace(/\[(\d+)\]/g, (match, digits: string) => {
    const ordinal = Number(digits)
    if (!allowed.has(ordinal)) return match
    return `<button type="button" class="citation-marker" data-citation="${ordinal}" aria-label="查看来源 ${ordinal}">${ordinal}</button>`
  })
}
