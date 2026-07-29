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
  createdAt: string
  updatedAt: string
}

export interface Message {
  id: string
  conversationId: string
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  think?: string
	structuredOutput?: unknown
  provider?: string
  model?: string
	status: 'streaming' | 'complete' | 'stopped' | 'interrupted' | 'error'
	finishReason?: string
  createdAt: string
}

export interface ModelProvider {
  id: string
  label: string
  models: string[]
}
