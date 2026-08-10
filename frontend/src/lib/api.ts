import type { Attachment, AuthSession, Conversation, CursorPage, Message, ModelProvider, User } from '@/types'

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
      ...(options.body && !(options.body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}),
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

export async function listConversations(options: { cursor?: string; query?: string; limit?: number } = {}): Promise<CursorPage<Conversation>> {
  const params = new URLSearchParams()
  if (options.cursor) params.set('cursor', options.cursor)
  if (options.query) params.set('q', options.query)
  if (options.limit) params.set('limit', String(options.limit))
  const suffix = params.size ? `?${params}` : ''
  const page = await request<{ conversations: Conversation[]; nextCursor: string; hasMore: boolean }>(`/api/v1/conversations${suffix}`)
  return { items: page.conversations, nextCursor: page.nextCursor, hasMore: page.hasMore }
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

export async function listMessages(conversationID: string, cursor = '', limit?: number): Promise<CursorPage<Message>> {
  const params = new URLSearchParams()
  if (cursor) params.set('cursor', cursor)
  if (limit) params.set('limit', String(limit))
  const suffix = params.size ? `?${params}` : ''
  const page = await request<{ messages: Message[]; nextCursor: string; hasMore: boolean }>(`/api/v1/conversations/${conversationID}/messages${suffix}`)
  return { items: page.messages, nextCursor: page.nextCursor, hasMore: page.hasMore }
}

export type ChatPayload = {
  content: string
  provider: string
  model: string
  temperature?: number
  outputFormat?: string
	attachmentIds?: string[]
}

export type RegeneratePayload = Omit<ChatPayload, 'content'>

// chatMessage is the non-streaming counterpart to streamMessage. It waits for
// the full model result, including any model-provided reasoning in `think`.
export async function chatMessage(conversationID: string, payload: ChatPayload, idempotencyKey = crypto.randomUUID()): Promise<{ userMessage: Message; assistantMessage: Message }> {
	return request<{ userMessage: Message; assistantMessage: Message }>(`/api/v1/conversations/${conversationID}/messages/chat`, {
		method: 'POST',
		headers: { 'Idempotency-Key': idempotencyKey },
		body: JSON.stringify(payload)
	})
}

export type StreamHandlers = {
  onMetadata: (userMessage: Message, assistantMessage: Message) => void
  onThink: (content: string) => void
  onDelta: (content: string) => void
  onReconnect: (attempt: number, maxRetries: number) => void
  onDone: (message: Message) => void
  onInterrupted: (message: Message, reason: string) => void
  onError: (message: string, assistantMessage?: Message) => void
}

export type UserEventHandlers = {
  onConversationTitleUpdated: (conversation: Conversation) => void
  onReset: () => void | Promise<void>
}

type StreamState = {
	messageID: string
	lastSequence: number
	terminal: boolean
}

type UserEventState = { lastSequence: number }

export async function subscribeUserEvents(handlers: UserEventHandlers, signal: AbortSignal): Promise<void> {
	const state: UserEventState = { lastSequence: 0 }
	let retry = 0
	while (!signal.aborted) {
		try {
			const response = await fetch(`/api/v1/events?afterSequence=${state.lastSequence}`, {
				headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
				signal
			})
			if (response.status === 409) {
				state.lastSequence = 0
				await handlers.onReset()
				continue
			}
			if (!response.ok || !response.body) {
				if (response.status === 401) throw new Error('登录状态已失效')
				throw new Error(`用户事件连接失败 (${response.status})`)
			}
			retry = 0
			await consumeUserEventStream(response, handlers, state, signal)
		} catch (cause) {
			if (signal.aborted) return
			if (cause instanceof Error && cause.message === '登录状态已失效') throw cause
			retry++
			await abortableDelay(Math.min(500 * 2 ** Math.min(retry - 1, 4), 5_000), signal)
		}
	}
}

async function consumeUserEventStream(response: Response, handlers: UserEventHandlers, state: UserEventState, signal: AbortSignal): Promise<void> {
	const reader = response.body!.getReader()
	const decoder = new TextDecoder()
	let buffer = ''
	while (!signal.aborted) {
		const { done, value } = await reader.read()
		if (done) throw new Error('用户事件连接已断开')
		buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')
		const frames = buffer.split('\n\n')
		buffer = frames.pop() || ''
		for (const frame of frames) {
			const event = frame.match(/^event:\s*(.+)$/m)?.[1]
			const raw = frame.match(/^data:\s*(.+)$/m)?.[1]
			if (!event || !raw) continue
			const data = JSON.parse(raw)
			if (event === 'stream.reset') {
				state.lastSequence = 0
				await handlers.onReset()
				return
			}
			const sequence = Number(data.sequence) || 0
			if (sequence <= state.lastSequence) continue
			state.lastSequence = sequence
			if (event === 'conversation.title.updated' && data.conversation) {
				handlers.onConversationTitleUpdated(data.conversation)
			}
		}
	}
}

export async function streamMessage(
  conversationID: string,
  payload: ChatPayload,
  handlers: StreamHandlers,
  signal: AbortSignal,
  idempotencyKey: string
): Promise<void> {
  return streamRequest(`/api/v1/conversations/${conversationID}/messages`, payload, handlers, signal, idempotencyKey)
}

export async function streamRegeneratedMessage(
  conversationID: string,
  messageID: string,
  payload: RegeneratePayload,
  handlers: StreamHandlers,
  signal: AbortSignal,
  idempotencyKey: string
): Promise<void> {
  return streamRequest(`/api/v1/conversations/${conversationID}/messages/${messageID}/regenerate`, payload, handlers, signal, idempotencyKey)
}

export async function streamEditedMessage(
  conversationID: string,
  messageID: string,
  payload: ChatPayload,
  handlers: StreamHandlers,
  signal: AbortSignal,
  idempotencyKey: string
): Promise<void> {
  return streamRequest(`/api/v1/conversations/${conversationID}/messages/${messageID}/edit`, payload, handlers, signal, idempotencyKey)
}

export async function activateBranch(conversationID: string, messageID: string): Promise<Message> {
  return (await request<{ message: Message }>(`/api/v1/conversations/${conversationID}/messages/${messageID}/activate`, { method: 'PATCH' })).message
}

async function streamRequest(
  path: string,
  payload: ChatPayload | RegeneratePayload,
  handlers: StreamHandlers,
  signal: AbortSignal,
  idempotencyKey: string
): Promise<void> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, Accept: 'text/event-stream', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(payload),
    signal
  })
  if (!response.ok || !response.body) {
    const body = await response.json().catch(() => ({ msg: `请求失败 (${response.status})` })) as Partial<ApiResponse<unknown>>
    throw new Error(body.msg || `请求失败 (${response.status})`)
  }
	const state: StreamState = { messageID: '', lastSequence: 0, terminal: false }
	try {
		await consumeEventStream(response, handlers, state, signal)
	} catch (cause) {
		if (signal.aborted || !state.messageID) throw cause
	}
	if (state.terminal || signal.aborted) return

	const maxReconnects = 3
	let lastError: unknown = new Error('与服务器的流式连接意外中断')
	for (let attempt = 1; attempt <= maxReconnects && !signal.aborted; attempt++) {
		handlers.onReconnect(attempt, maxReconnects)
		if (attempt > 1) await abortableDelay(500 * 2 ** (attempt - 2), signal)
		try {
			const resumed = await fetch(`/api/v1/messages/${state.messageID}/stream?afterSequence=${state.lastSequence}`, {
				headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
				signal
			})
			if (!resumed.ok || !resumed.body) {
				const body = await resumed.json().catch(() => ({ msg: `重连失败 (${resumed.status})` })) as Partial<ApiResponse<unknown>>
				throw new Error(body.msg || `重连失败 (${resumed.status})`)
			}
			await consumeEventStream(resumed, handlers, state, signal)
			if (state.terminal) return
		} catch (cause) {
			if (signal.aborted) return
			lastError = cause
		}
	}
	throw lastError
}

async function consumeEventStream(response: Response, handlers: StreamHandlers, state: StreamState, signal: AbortSignal): Promise<void> {
	if (!response.body) throw new Error('服务器未返回流式响应')
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
		state.messageID = data.messageId || state.messageID
		state.lastSequence = Math.max(state.lastSequence, Number(data.sequence) || 0)
        if (event === 'metadata') handlers.onMetadata(data.userMessage, data.assistantMessage)
        if (event === 'think') handlers.onThink(data.content)
        if (event === 'delta') handlers.onDelta(data.content)
        if (event === 'done') {
		  state.terminal = true
          handlers.onDone(data.assistantMessage)
        }
		if (event === 'interrupted') {
		  state.terminal = true
		  handlers.onInterrupted(data.assistantMessage, data.reason)
		}
        if (event === 'error') {
		  state.terminal = true
          handlers.onError(data.message, data.assistantMessage)
        }
      } catch {
        handlers.onError('无法解析模型返回的数据')
      }
    }
  }
	if (!state.terminal && !signal.aborted) throw new Error('与服务器的流式连接意外中断')
}

function abortableDelay(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((resolve, reject) => {
		const timer = window.setTimeout(resolve, ms)
		signal.addEventListener('abort', () => {
			window.clearTimeout(timer)
			reject(new DOMException('Aborted', 'AbortError'))
		}, { once: true })
	})
}

export async function uploadAttachment(conversationID: string, file: File): Promise<Attachment> {
	const body = new FormData()
	body.append('file', file)
	return (await request<{ attachment: Attachment }>(`/api/v1/conversations/${conversationID}/attachments`, { method: 'POST', body })).attachment
}

export async function retryAttachment(conversationID: string, attachmentID: string): Promise<Attachment> {
	return (await request<{ attachment: Attachment }>(`/api/v1/conversations/${conversationID}/attachments/${attachmentID}/retry`, { method: 'POST' })).attachment
}

export async function deleteAttachment(conversationID: string, attachmentID: string): Promise<void> {
	await request<unknown>(`/api/v1/conversations/${conversationID}/attachments/${attachmentID}`, { method: 'DELETE' })
}

export async function downloadAttachment(conversationID: string, attachment: Attachment): Promise<void> {
	const response = await fetch(`/api/v1/conversations/${conversationID}/attachments/${attachment.id}/content`, { headers: { Authorization: `Bearer ${token}` } })
	if (!response.ok) throw new Error(`下载失败 (${response.status})`)
	const url = URL.createObjectURL(await response.blob())
	const anchor = document.createElement('a'); anchor.href = url; anchor.download = attachment.name; anchor.click()
	URL.revokeObjectURL(url)
}
