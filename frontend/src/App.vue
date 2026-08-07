<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowReactive } from 'vue'
import AuthPanel from '@/components/AuthPanel.vue'
import ChatPanel from '@/components/ChatPanel.vue'
import ConversationSidebar from '@/components/ConversationSidebar.vue'
import {
  activateBranch,
  clearSession,
  createConversation,
  deleteConversation,
  getMe,
  listConversations,
  listMessages,
  listProviders,
  renameConversation,
  restoreSession,
  saveSession,
  streamMessage,
  streamEditedMessage,
  streamRegeneratedMessage,
	subscribeUserEvents,
  type StreamHandlers
} from '@/lib/api'
import type { AuthSession, Conversation, Message, ModelProvider, User } from '@/types'

const initialSession = restoreSession()
const user = ref<User | null>(initialSession?.user ?? null)
const conversations = ref<Conversation[]>([])
const activeID = ref<string | null>(null)
const messages = ref<Message[]>([])
const providers = ref<ModelProvider[]>([])
const booting = ref(Boolean(initialSession))
const error = ref('')
const streamNotice = ref('')
const streamControllers = shallowReactive(new Map<string, AbortController>())
const liveTurns = reactive(new Map<string, { userMessage: Message; assistantMessage: Message }>())
let userEventsController: AbortController | null = null

const activeConversation = computed(() => conversations.value.find((item) => item.id === activeID.value) ?? null)
const pending = computed(() => Boolean(activeID.value && streamControllers.has(activeID.value)))

onMounted(async () => {
  if (initialSession) {
    try {
      user.value = await getMe()
      await loadWorkspace()
	  startUserEvents()
    } catch {
      logout()
    } finally {
      booting.value = false
    }
  }
})

onBeforeUnmount(() => {
	abortAllStreams()
	userEventsController?.abort()
})

async function authenticated(session: AuthSession) {
  saveSession(session)
  user.value = session.user
  try {
    await loadWorkspace()
	startUserEvents()
  } catch {
    logout()
  }
}

async function loadWorkspace() {
  error.value = ''
  const [conversationItems, providerItems] = await Promise.all([listConversations(), listProviders()])
  conversations.value = conversationItems
  providers.value = providerItems
  if (conversationItems.length) await selectConversation(conversationItems[0].id)
}

async function createNewConversation() {
  try {
    const conversation = await createConversation()
    conversations.value = [conversation, ...conversations.value]
    activeID.value = conversation.id
    messages.value = []
  } catch (cause) {
    error.value = messageFor(cause)
  }
}

async function selectConversation(id: string) {
  activeID.value = id
  error.value = ''
	streamNotice.value = ''
  try {
	  messages.value = mergeLiveTurn(await listMessages(id), id)
  } catch (cause) {
    error.value = messageFor(cause)
  }
}

async function updateTitle(conversation: Conversation, done?: (success: boolean, message?: string) => void) {
  try {
    const updated = await renameConversation(conversation.id, conversation.title)
    conversations.value = conversations.value.map((item) => item.id === updated.id ? updated : item)
    done?.(true)
  } catch (cause) {
    const message = messageFor(cause)
    error.value = message
    done?.(false, message)
  }
}

async function removeConversation(conversation: Conversation) {
	streamControllers.get(conversation.id)?.abort()
  try {
    await deleteConversation(conversation.id)
    conversations.value = conversations.value.filter((item) => item.id !== conversation.id)
    if (activeID.value === conversation.id) {
      activeID.value = conversations.value[0]?.id ?? null
      messages.value = []
      if (activeID.value) await selectConversation(activeID.value)
    }
  } catch (cause) {
    error.value = messageFor(cause)
  }
}

async function send(content: string, provider: string, model: string) {
	if (!activeID.value) return
	const conversationID = activeID.value
	await runStreamingRequest(conversationID, (handlers, signal, idempotencyKey) =>
		streamMessage(conversationID, { content, provider, model }, handlers, signal, idempotencyKey))
}

async function regenerate(message: Message, provider: string, model: string) {
	if (!activeID.value || message.role !== 'assistant') return
	const conversationID = activeID.value
	linkLegacyResponseToUser(message)
	await runStreamingRequest(conversationID, (handlers, signal, idempotencyKey) =>
		streamRegeneratedMessage(conversationID, message.id, { provider, model }, handlers, signal, idempotencyKey))
}

async function editMessage(message: Message, content: string, provider: string, model: string) {
	if (!activeID.value || message.role !== 'user') return
	const conversationID = activeID.value
	await runStreamingRequest(conversationID, (handlers, signal, idempotencyKey) =>
		streamEditedMessage(conversationID, message.id, { content, provider, model }, handlers, signal, idempotencyKey))
}

function linkLegacyResponseToUser(message: Message) {
	if (message.replyToMessageId) return
	const index = messages.value.findIndex((item) => item.id === message.id)
	for (let current = index - 1; current >= 0; current--) {
		if (messages.value[current].role !== 'user') continue
		messages.value = messages.value.map((item) => item.id === message.id ? {
			...item,
			parentMessageId: messages.value[current].id,
			isActiveChild: true,
			replyToMessageId: messages.value[current].id,
			responseVersion: 1,
			isActiveResponse: true
		} : item)
		return
	}
}

async function switchBranch(message: Message) {
	if (!activeID.value || pending.value) return
	try {
		const selected = await activateBranch(activeID.value, message.id)
		markBranchActive(selected)
	} catch (cause) {
		error.value = messageFor(cause)
	}
}

async function runStreamingRequest(
	conversationID: string,
	start: (handlers: StreamHandlers, signal: AbortSignal, idempotencyKey: string) => Promise<void>
) {
	if (streamControllers.has(conversationID)) return
	const controller = new AbortController()
	const idempotencyKey = crypto.randomUUID()
  error.value = ''
  streamNotice.value = ''
  streamControllers.set(conversationID, controller)
  let receivedDone = false
  try {
	await start({
      onMetadata(userMessage, assistantMessage) {
		markBranchActive(userMessage)
		markBranchActive(assistantMessage)
		liveTurns.set(conversationID, { userMessage, assistantMessage })
		if (activeID.value === conversationID) {
		  messages.value = mergeLiveTurn(messages.value, conversationID)
		}
      },
      onThink(delta) {
		const turn = liveTurns.get(conversationID)
		if (turn) turn.assistantMessage.think = (turn.assistantMessage.think ?? '') + delta
		if (activeID.value === conversationID) streamNotice.value = ''
      },
      onDelta(delta) {
		const turn = liveTurns.get(conversationID)
		if (turn) turn.assistantMessage.content += delta
		if (activeID.value === conversationID) streamNotice.value = ''
      },
      onDone(message) {
        receivedDone = true
		liveTurns.delete(conversationID)
		if (activeID.value === conversationID) {
		  streamNotice.value = ''
		  replaceMessage(message)
		}
      },
      onReconnect(attempt, maxRetries) {
		if (activeID.value === conversationID) streamNotice.value = `连接中断，正在补发缓存片段（${attempt}/${maxRetries}）…`
      },
      onInterrupted(message, reason) {
		receivedDone = true
		liveTurns.delete(conversationID)
		if (activeID.value !== conversationID) return
		streamNotice.value = ''
		replaceMessage(message)
		if (message.status === 'interrupted') error.value = reason === 'ttft_timeout'
			? '等待模型响应超时，本次回答已中断，可重新生成。'
			: '这次回答已中断，可重新生成。'
      },
      onError(message, assistantMessage) {
		const turn = liveTurns.get(conversationID)
		if (assistantMessage) liveTurns.delete(conversationID)
		else if (turn) turn.assistantMessage.status = 'error'
		if (activeID.value !== conversationID) return
		streamNotice.value = ''
        error.value = message
		if (assistantMessage) replaceMessage(assistantMessage)
        const latest = messages.value[messages.value.length - 1]
        if (latest?.role === 'assistant') latest.status = 'error'
      }
	}, controller.signal, idempotencyKey)
	if (activeID.value === conversationID && !receivedDone && !error.value && messages.value.at(-1)?.role === 'assistant') {
      messages.value[messages.value.length - 1].status = 'stopped'
    }
  } catch (cause) {
	const turn = liveTurns.get(conversationID)
	if (turn) turn.assistantMessage.status = controller.signal.aborted ? 'stopped' : 'error'
	if (activeID.value === conversationID) {
	  streamNotice.value = ''
	  if (!controller.signal.aborted) error.value = messageFor(cause)
	}
  } finally {
	if (streamControllers.get(conversationID) === controller) streamControllers.delete(conversationID)
	if (!receivedDone) liveTurns.delete(conversationID)
	refreshConversationOrder(conversationID)
  }
}

function stop() {
	if (activeID.value) streamControllers.get(activeID.value)?.abort()
}

function replaceMessage(updated: Message) {
	markBranchActive(updated)
  messages.value = messages.value.map((item) => item.id === updated.id ? updated : item)
}

function markBranchActive(selected: Message) {
	if (!selected.isActiveChild) return
	messages.value = messages.value.map((item) => {
		const sameParent = item.parentMessageId === selected.parentMessageId
		if (item.role !== selected.role || !sameParent) return item
		const active = item.id === selected.id
		return {
			...item,
			isActiveChild: active,
			...(item.role === 'assistant' ? { isActiveResponse: active } : {})
		}
	})
}

function mergeLiveTurn(items: Message[], conversationID: string): Message[] {
	const turn = liveTurns.get(conversationID)
	if (!turn) return items
	const replacements = new Map([
		[turn.userMessage.id, turn.userMessage],
		[turn.assistantMessage.id, turn.assistantMessage]
	])
	const merged = items.map((item) => replacements.get(item.id) ?? item)
	for (const message of [turn.userMessage, turn.assistantMessage]) {
		if (!merged.some((item) => item.id === message.id)) merged.push(message)
	}
	return merged.sort((a, b) => a.createdAt.localeCompare(b.createdAt))
}

function startUserEvents() {
	userEventsController?.abort()
	const controller = new AbortController()
	userEventsController = controller
	void subscribeUserEvents({
		onConversationTitleUpdated(conversation) {
			conversations.value = conversations.value
				.map((item) => item.id === conversation.id ? conversation : item)
				.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
		},
		async onReset() {
			if (!user.value) return
			conversations.value = await listConversations()
		}
	}, controller.signal).catch((cause) => {
		if (!controller.signal.aborted) error.value = messageFor(cause)
	})
}

function refreshConversationOrder(id: string) {
  const conversation = conversations.value.find((item) => item.id === id)
  if (!conversation) return
  conversation.updatedAt = new Date().toISOString()
  conversations.value = [...conversations.value].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
}

function logout() {
	abortAllStreams()
	userEventsController?.abort()
	userEventsController = null
  clearSession()
  user.value = null
  conversations.value = []
  activeID.value = null
  messages.value = []
  providers.value = []
  error.value = ''
  streamNotice.value = ''
	liveTurns.clear()
}

function abortAllStreams() {
	for (const controller of streamControllers.values()) controller.abort()
	streamControllers.clear()
}

function messageFor(cause: unknown) { return cause instanceof Error ? cause.message : '操作失败，请稍后再试' }
</script>

<template>
  <div v-if="booting" class="loading-page"><span class="loading-mark" />正在恢复工作台…</div>
  <AuthPanel v-else-if="!user" @authenticated="authenticated" />
  <div v-else class="app-shell">
    <ConversationSidebar
      :conversations="conversations"
      :active-i-d="activeID"
      :user="user"
      @create="createNewConversation"
      @select="selectConversation"
      @rename="updateTitle"
      @remove="removeConversation"
      @logout="logout"
    />
    <ChatPanel
      :conversation="activeConversation"
      :messages="messages"
      :providers="providers"
      :pending="pending"
      :error="error"
      :notice="streamNotice"
      @send="send"
	  @regenerate="regenerate"
	  @edit-message="editMessage"
	  @switch-version="switchBranch"
      @stop="stop"
    />
  </div>
</template>
