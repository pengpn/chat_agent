export interface User {
  id: string
  email: string
  displayName: string
  createdAt: string
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
