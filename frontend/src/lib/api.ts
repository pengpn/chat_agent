import type { AuthSession, Conversation, Message, ModelProvider, User } from '@/types'

const SESSION_KEY = 'agent-platform.session'
let token = ''

export function restoreSession(): AuthSession | null {
  try {
    const raw = localStorage.getItem(SESSION_KEY)
    if (!raw) return null
    const session = JSON.parse(raw) as AuthSession
    if (new Date(session.expiresAt) <= new Date()) {
      localStorage.removeItem(SESSION_KEY)
      return null
    }
    token = session.accessToken
    return session
  } catch {
    localStorage.removeItem(SESSION_KEY)
    return null
  }
}

export function saveSession(session: AuthSession): void {
  token = session.accessToken
  localStorage.setItem(SESSION_KEY, JSON.stringify(session))
}

export function clearSession(): void {
  token = ''
  localStorage.removeItem(SESSION_KEY)
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options.headers
    }
  })
  if (response.status === 204) return undefined as T
  const body = await response.json().catch(() => ({ code: -1, msg: `请求失败 (${response.status})` })) as ApiResponse<T>
  if (!response.ok || body.code !== 0) throw new Error(body.msg || `请求失败 (${response.status})`)
  return body.data
}

interface ApiResponse<T> {
  code: number
  msg: string
  data: T
}

export async function register(email: string, password: string, displayName: string): Promise<AuthSession> {
  return request<AuthSession>('/api/v1/auth/register', { method: 'POST', body: JSON.stringify({ email, password, displayName }) })
}

export async function login(email: string, password: string): Promise<AuthSession> {
  return request<AuthSession>('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ email, password }) })
}

export async function getMe(): Promise<User> {
  return (await request<{ user: User }>('/api/v1/auth/me')).user
}

export async function listProviders(): Promise<ModelProvider[]> {
  return (await request<{ providers: ModelProvider[] }>('/api/v1/models')).providers
}

export async function listConversations(): Promise<Conversation[]> {
  return (await request<{ conversations: Conversation[] }>('/api/v1/conversations')).conversations
}

export async function createConversation(title = ''): Promise<Conversation> {
  return (await request<{ conversation: Conversation }>('/api/v1/conversations', { method: 'POST', body: JSON.stringify({ title }) })).conversation
}

export async function renameConversation(id: string, title: string): Promise<Conversation> {
  return (await request<{ conversation: Conversation }>(`/api/v1/conversations/${id}`, { method: 'PATCH', body: JSON.stringify({ title }) })).conversation
}

export async function deleteConversation(id: string): Promise<void> {
  await request<unknown>(`/api/v1/conversations/${id}`, { method: 'DELETE' })
}

export async function listMessages(conversationID: string): Promise<Message[]> {
  return (await request<{ messages: Message[] }>(`/api/v1/conversations/${conversationID}/messages`)).messages
}

export type ChatPayload = { content: string; provider: string; model: string; temperature?: number }

// chatMessage is the non-streaming counterpart to streamMessage. It waits for
// the full model result, including any model-provided reasoning in `think`.
export async function chatMessage(conversationID: string, payload: ChatPayload): Promise<{ userMessage: Message; assistantMessage: Message }> {
  return request<{ userMessage: Message; assistantMessage: Message }>(`/api/v1/conversations/${conversationID}/messages/chat`, {
    method: 'POST',
    body: JSON.stringify(payload)
  })
}

type StreamHandlers = {
  onMetadata: (userMessage: Message, assistantMessage: Message) => void
  onThink: (content: string) => void
  onDelta: (content: string) => void
  onDone: (message: Message) => void
  onError: (message: string) => void
}

export async function streamMessage(
  conversationID: string,
  payload: ChatPayload,
  handlers: StreamHandlers,
  signal: AbortSignal
): Promise<void> {
  const response = await fetch(`/api/v1/conversations/${conversationID}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
    body: JSON.stringify(payload),
    signal
  })
  if (!response.ok || !response.body) {
    const body = await response.json().catch(() => ({ msg: `请求失败 (${response.status})` })) as Partial<ApiResponse<unknown>>
    throw new Error(body.msg || `请求失败 (${response.status})`)
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')
    const frames = buffer.split('\n\n')
    buffer = frames.pop() || ''
    for (const frame of frames) {
      const event = frame.match(/^event:\s*(.+)$/m)?.[1]
      const raw = frame.match(/^data:\s*(.+)$/m)?.[1]
      if (!event || !raw) continue
      try {
        const data = JSON.parse(raw)
        if (event === 'metadata') handlers.onMetadata(data.userMessage, data.assistantMessage)
        if (event === 'think') handlers.onThink(data.content)
        if (event === 'delta') handlers.onDelta(data.content)
        if (event === 'done') handlers.onDone(data.assistantMessage)
        if (event === 'error') handlers.onError(data.message)
      } catch {
        handlers.onError('无法解析模型返回的数据')
      }
    }
  }
}
