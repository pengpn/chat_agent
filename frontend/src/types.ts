export interface User {
  id: string
  email: string
  displayName: string
  emailVerifiedAt?: string
  createdAt: string
}

export interface DeviceSession {
  id: string
  deviceName: string
  current: boolean
  createdAt: string
  lastUsedAt: string
  expiresAt: string
}

export interface AuthSession {
  user: User
  accessToken: string
  expiresAt: string
}

export interface Conversation {
  id: string
  title: string
  titleStatus?: 'default' | 'generating' | 'generated' | 'fallback' | 'manual'
  createdAt: string
  updatedAt: string
}

export interface Message {
  id: string
  conversationId: string
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  think?: string
  parentMessageId?: string
  isActiveChild?: boolean
  editedFromMessageId?: string
  replyToMessageId?: string
  responseVersion?: number
  isActiveResponse?: boolean
  regeneratedFromMessageId?: string
  structuredOutput?: unknown
  provider?: string
  model?: string
  status: 'streaming' | 'complete' | 'stopped' | 'interrupted' | 'error'
  finishReason?: string
  createdAt: string
	attachments?: Attachment[]
	knowledgeBaseIds?: string[]
	citations?: MessageCitation[]
}

export interface KnowledgeBase {
  id: string
  name: string
  description: string
  createdAt: string
  updatedAt: string
}

export type KnowledgeDocumentStatus = 'uploaded' | 'extracting' | 'chunking' | 'embedding' | 'ready' | 'failed'

export interface KnowledgeDocument {
  id: string
  knowledgeBaseId: string
  name: string
  extension: string
  mimeType: string
  size: number
  checksum: string
  status: KnowledgeDocumentStatus
  errorCode?: string
  errorMessage?: string
  chunkCount: number
  ingestionVersion: number
  indexedAt?: string
  createdAt: string
  updatedAt: string
}

export interface MessageCitation {
  id?: string
  messageId?: string
  knowledgeBaseId?: string
  documentId?: string
  chunkId?: string
  ordinal: number
  sourceName: string
  pageNumber?: number
  sectionTitle?: string
  quote: string
  similarityScore: number
  createdAt?: string
}

export interface Attachment {
	id: string
	conversationId: string
	messageId?: string
	name: string
	extension: string
	mimeType: string
	size: number
	kind: 'image' | 'document'
	status: 'uploaded' | 'processing' | 'ready' | 'failed'
	errorCode?: string
	errorMessage?: string
	chunkStatus: string
	embeddingStatus: string
	createdAt: string
	updatedAt: string
}

export interface ModelProvider {
  id: string
  label: string
  models: string[]
	visionModels: string[]
}

export interface CursorPage<T> {
  items: T[]
  nextCursor: string
  hasMore: boolean
}
