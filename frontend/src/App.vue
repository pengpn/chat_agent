<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowReactive } from 'vue'
import AuthPanel from '@/components/AuthPanel.vue'
import AccountPanel from '@/components/AccountPanel.vue'
import ChatPanel from '@/components/ChatPanel.vue'
import ConversationSidebar from '@/components/ConversationSidebar.vue'
import KnowledgePanel from '@/components/KnowledgePanel.vue'
import { mergeConversations, mergeMessages, sortMessagesAscending } from '@/lib/list-pagination'
import {
  activateBranch,
  clearSession,
  createConversation,
  deleteConversation,
  getMe,
  listConversations,
  listMessages,
  listKnowledgeBases,
  listProviders,
  logoutSession,
  refreshAccessToken,
  renameConversation,
  restoreSession,
  saveSession,
  streamMessage,
  streamEditedMessage,
  streamRegeneratedMessage,
	subscribeUserEvents,
  updateStoredUser,
  type StreamHandlers
} from '@/lib/api'
import type { AuthSession, Conversation, KnowledgeBase, Message, ModelProvider, User } from '@/types'

const initialSession = restoreSession()
const initialURL = new URL(window.location.href)
const initialVerifyToken = initialURL.searchParams.get('verifyToken')
const initialResetToken = initialURL.searchParams.get('resetToken')
const accountAction = ref<'verify' | 'reset' | null>(initialVerifyToken ? 'verify' : initialResetToken ? 'reset' : null)
const accountToken = ref(initialVerifyToken ?? initialResetToken ?? '')
const user = ref<User | null>(initialSession?.user ?? null)
const accountOpen = ref(false)
const agentRevision = ref(0)
const conversations = ref<Conversation[]>([])
const conversationCursor = ref('')
const conversationsHaveMore = ref(false)
const conversationsLoadingMore = ref(false)
const conversationsSearching = ref(false)
const conversationSearch = ref('')
const activeID = ref<string | null>(null)
const activeConversationSnapshot = ref<Conversation | null>(null)
const messages = ref<Message[]>([])
const messageCursor = ref('')
const messagesHaveMore = ref(false)
const messagesLoadingOlder = ref(false)
const messagesLoading = ref(false)
const providers = ref<ModelProvider[]>([])
const knowledgeBases = ref<KnowledgeBase[]>([])
const knowledgeError = ref('')
const workspace = ref<'chat' | 'knowledge'>('chat')
const booting = ref(true)
const error = ref('')
const streamNotice = ref('')
const streamControllers = shallowReactive(new Map<string, AbortController>())
const liveTurns = reactive(new Map<string, { userMessage: Message; assistantMessage: Message }>())
let userEventsController: AbortController | null = null
let conversationRequestVersion = 0
let messageRequestVersion = 0
let accountTrigger: HTMLElement | null = null

const activeConversation = computed(() => conversations.value.find((item) => item.id === activeID.value)
  ?? (activeConversationSnapshot.value?.id === activeID.value ? activeConversationSnapshot.value : null))
const pending = computed(() => Boolean(activeID.value && streamControllers.has(activeID.value)))

onMounted(async () => {
  try {
    const session = initialSession ?? await refreshAccessToken()
    user.value = await getMe()
    if (session.user.emailVerifiedAt !== user.value.emailVerifiedAt) updateStoredUser(user.value)
    if (!accountAction.value) {
      await loadWorkspace()
	  startUserEvents()
    }
  } catch {
    clearLocalState()
  } finally {
    booting.value = false
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

async function completeAccountAction(action: 'verify' | 'reset', verifiedUser?: User) {
  accountAction.value = null
  accountToken.value = ''
  const url = new URL(window.location.href)
  url.searchParams.delete('verifyToken')
  url.searchParams.delete('resetToken')
  window.history.replaceState({}, '', `${url.pathname}${url.search}${url.hash}`)
  if (action === 'reset') {
    clearLocalState()
    return
  }
  if (!user.value || !verifiedUser) return
  user.value = verifiedUser
  updateStoredUser(verifiedUser)
  if (!providers.value.length) {
    await loadWorkspace()
    startUserEvents()
  }
}

function openAccount() {
  accountTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  accountOpen.value = true
}

async function closeAccount() {
  accountOpen.value = false
  await nextTick()
  accountTrigger?.focus()
  accountTrigger = null
}

async function loadWorkspace() {
  error.value = ''
  conversationSearch.value = ''
  const [conversationPage, providerItems] = await Promise.all([listConversations(), listProviders(), loadKnowledgeBases()])
  conversations.value = conversationPage.items
  conversationCursor.value = conversationPage.nextCursor
  conversationsHaveMore.value = conversationPage.hasMore
  providers.value = providerItems
  if (conversationPage.items.length) await selectConversation(conversationPage.items[0].id)
}

async function createNewConversation() {
  workspace.value = 'chat'
  try {
    const conversation = await createConversation()
    conversationSearch.value = ''
    const page = await listConversations()
    conversations.value = page.items
    conversationCursor.value = page.nextCursor
    conversationsHaveMore.value = page.hasMore
    activeID.value = conversation.id
    activeConversationSnapshot.value = conversation
    messages.value = []
    messageCursor.value = ''
    messagesHaveMore.value = false
    messagesLoading.value = false
  } catch (cause) {
    error.value = messageFor(cause)
  }
}

async function selectConversation(id: string) {
  workspace.value = 'chat'
  const requestVersion = ++messageRequestVersion
  activeID.value = id
  activeConversationSnapshot.value = conversations.value.find((item) => item.id === id) ?? activeConversationSnapshot.value
  error.value = ''
	streamNotice.value = ''
  messagesLoadingOlder.value = false
  messagesLoading.value = true
  messages.value = mergeLiveTurn([], id)
  messageCursor.value = ''
  messagesHaveMore.value = false
  try {
	  const page = await listMessages(id)
	  if (activeID.value !== id || requestVersion !== messageRequestVersion) return
	  messages.value = mergeLiveTurn(sortMessagesAscending(page.items), id)
	  messageCursor.value = page.nextCursor
	  messagesHaveMore.value = page.hasMore
  } catch (cause) {
    if (activeID.value === id && requestVersion === messageRequestVersion) error.value = messageFor(cause)
  } finally {
    if (activeID.value === id && requestVersion === messageRequestVersion) messagesLoading.value = false
  }
}

async function searchConversations(query: string) {
  const normalized = query.trim()
  conversationSearch.value = normalized
  const requestVersion = ++conversationRequestVersion
  conversationsSearching.value = true
  try {
    const page = await listConversations({ query: normalized })
    if (requestVersion !== conversationRequestVersion || conversationSearch.value !== normalized) return
    conversations.value = page.items
    conversationCursor.value = page.nextCursor
    conversationsHaveMore.value = page.hasMore
  } catch (cause) {
    if (requestVersion === conversationRequestVersion) error.value = messageFor(cause)
  } finally {
    if (requestVersion === conversationRequestVersion) conversationsSearching.value = false
  }
}

async function loadMoreConversations() {
  if (conversationsLoadingMore.value || !conversationsHaveMore.value || !conversationCursor.value) return
  conversationsLoadingMore.value = true
  const cursor = conversationCursor.value
  const query = conversationSearch.value
  try {
    const page = await listConversations({ cursor, query })
    if (query !== conversationSearch.value) return
    conversations.value = mergeConversations(conversations.value, page.items)
    conversationCursor.value = page.nextCursor
    conversationsHaveMore.value = page.hasMore
  } catch (cause) {
    error.value = messageFor(cause)
  } finally {
    conversationsLoadingMore.value = false
  }
}

async function loadOlderMessages(done: () => void) {
  const conversationID = activeID.value
  if (!conversationID || messagesLoadingOlder.value || !messagesHaveMore.value || !messageCursor.value) {
    done()
    return
  }
  messagesLoadingOlder.value = true
  const requestVersion = messageRequestVersion
  const cursor = messageCursor.value
  try {
    const page = await listMessages(conversationID, cursor)
    if (activeID.value !== conversationID) return
    messages.value = mergeMessages(sortMessagesAscending(page.items), messages.value)
    messageCursor.value = page.nextCursor
    messagesHaveMore.value = page.hasMore
  } catch (cause) {
    if (activeID.value === conversationID) error.value = messageFor(cause)
  } finally {
    if (activeID.value === conversationID && requestVersion === messageRequestVersion) messagesLoadingOlder.value = false
    done()
  }
}

async function updateTitle(conversation: Conversation, done?: (success: boolean, message?: string) => void) {
  try {
    const updated = await renameConversation(conversation.id, conversation.title)
    conversations.value = conversations.value.map((item) => item.id === updated.id ? updated : item)
    if (activeID.value === updated.id) activeConversationSnapshot.value = updated
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
      activeConversationSnapshot.value = conversations.value[0] ?? null
      messages.value = []
      messageCursor.value = ''
      messagesHaveMore.value = false
      if (activeID.value) await selectConversation(activeID.value)
    }
  } catch (cause) {
    error.value = messageFor(cause)
  }
}

async function loadKnowledgeBases() {
  knowledgeError.value = ''
  try {
    knowledgeBases.value = await listKnowledgeBases()
  } catch (cause) {
    knowledgeError.value = messageFor(cause)
  }
}

async function send(content: string, provider: string, model: string, attachmentIDs: string[], knowledgeBaseIDs: string[]) {
	if (!activeID.value) return
	const conversationID = activeID.value
	await runStreamingRequest(conversationID, (handlers, signal, idempotencyKey) =>
		streamMessage(conversationID, { content, provider, model, attachmentIds: attachmentIDs, knowledgeBaseIds: knowledgeBaseIDs }, handlers, signal, idempotencyKey))
}

async function regenerate(message: Message, provider: string, model: string) {
	if (!activeID.value || message.role !== 'assistant') return
	const conversationID = activeID.value
	linkLegacyResponseToUser(message)
	await runStreamingRequest(conversationID, (handlers, signal, idempotencyKey) =>
		streamRegeneratedMessage(conversationID, message.id, { provider, model }, handlers, signal, idempotencyKey))
}

async function editMessage(message: Message, content: string, provider: string, model: string, knowledgeBaseIDs: string[]) {
	if (!activeID.value || message.role !== 'user') return
	const conversationID = activeID.value
	await runStreamingRequest(conversationID, (handlers, signal, idempotencyKey) =>
		streamEditedMessage(conversationID, message.id, { content, provider, model, knowledgeBaseIds: knowledgeBaseIDs }, handlers, signal, idempotencyKey))
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
      onCitations(citations) {
        const turn = liveTurns.get(conversationID)
        if (turn) turn.assistantMessage.citations = citations
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
	return sortMessagesAscending(merged)
}

function startUserEvents() {
	userEventsController?.abort()
	const controller = new AbortController()
	userEventsController = controller
	void subscribeUserEvents({
		onAgentStateChanged() { agentRevision.value++ },
		onConversationTitleUpdated(conversation) {
			if (activeID.value === conversation.id) activeConversationSnapshot.value = conversation
			if (conversationSearch.value) {
				void searchConversations(conversationSearch.value)
				return
			}
			conversations.value = conversations.value
				.map((item) => item.id === conversation.id ? conversation : item)
				.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
		},
		async onReset() {
			if (!user.value) return
			const page = await listConversations({ query: conversationSearch.value })
			conversations.value = page.items
			conversationCursor.value = page.nextCursor
			conversationsHaveMore.value = page.hasMore
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

async function logout() {
	try {
		await logoutSession()
	} catch {
		// 无论服务端是否可达，都清理本地凭证，避免失效会话残留在界面中。
	}
	clearLocalState()
}

function clearLocalState() {
	abortAllStreams()
	userEventsController?.abort()
	userEventsController = null
  clearSession()
  accountOpen.value = false
  user.value = null
  conversations.value = []
  conversationCursor.value = ''
  conversationsHaveMore.value = false
  conversationsSearching.value = false
  conversationSearch.value = ''
  activeID.value = null
  activeConversationSnapshot.value = null
  messages.value = []
  messageCursor.value = ''
  messagesHaveMore.value = false
  messagesLoading.value = false
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
  <AuthPanel
    v-else-if="accountAction || !user"
    :initial-action="accountAction ?? undefined"
    :action-token="accountToken"
    :has-session="Boolean(user)"
    @authenticated="authenticated"
    @action-complete="completeAccountAction"
  />
  <div v-else class="app-shell">
    <ConversationSidebar
      :conversations="conversations"
      :active-i-d="activeID"
      :user="user"
      :has-more="conversationsHaveMore"
      :loading-more="conversationsLoadingMore"
      :searching="conversationsSearching"
      :search-query="conversationSearch"
      @create="createNewConversation"
      @select="selectConversation"
      @rename="updateTitle"
      @remove="removeConversation"
      @search="searchConversations"
      @load-more="loadMoreConversations"
      @account="openAccount"
      @logout="logout"
      @open-knowledge="workspace = 'knowledge'"
    />
    <KnowledgePanel v-if="workspace === 'knowledge'" @back="workspace = 'chat'" @changed="loadKnowledgeBases" />
    <ChatPanel
      v-else
      :conversation="activeConversation"
      :messages="messages"
      :providers="providers"
      :pending="pending"
      :error="error"
      :notice="streamNotice"
      :has-older-messages="messagesHaveMore"
      :loading-older-messages="messagesLoadingOlder"
      :loading-messages="messagesLoading"
      :knowledge-bases="knowledgeBases"
      :knowledge-error="knowledgeError"
      :agent-revision="agentRevision"
      @send="send"
	  @regenerate="regenerate"
	  @edit-message="editMessage"
      @switch-version="switchBranch"
      @load-older="loadOlderMessages"
      @account="openAccount"
      @stop="stop"
      @reload-knowledge="loadKnowledgeBases"
      @open-knowledge="workspace = 'knowledge'"
    />
    <AccountPanel
      v-if="accountOpen"
      :user="user"
      @close="closeAccount"
      @signed-out="clearLocalState"
      @deleted="clearLocalState"
    />
  </div>
</template>
