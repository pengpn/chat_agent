<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AuthPanel from '@/components/AuthPanel.vue'
import ChatPanel from '@/components/ChatPanel.vue'
import ConversationSidebar from '@/components/ConversationSidebar.vue'
import {
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
  streamMessage
} from '@/lib/api'
import type { AuthSession, Conversation, Message, ModelProvider, User } from '@/types'

const initialSession = restoreSession()
const user = ref<User | null>(initialSession?.user ?? null)
const conversations = ref<Conversation[]>([])
const activeID = ref<string | null>(null)
const messages = ref<Message[]>([])
const providers = ref<ModelProvider[]>([])
const booting = ref(Boolean(initialSession))
const pending = ref(false)
const error = ref('')
let abortController: AbortController | null = null

const activeConversation = computed(() => conversations.value.find((item) => item.id === activeID.value) ?? null)

onMounted(async () => {
  if (initialSession) {
    try {
      user.value = await getMe()
      await loadWorkspace()
    } catch {
      logout()
    } finally {
      booting.value = false
    }
  }
})

async function authenticated(session: AuthSession) {
  saveSession(session)
  user.value = session.user
  try {
    await loadWorkspace()
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
  if (pending.value) return
  activeID.value = id
  error.value = ''
  try {
    messages.value = await listMessages(id)
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
  if (!activeID.value || pending.value) return
  error.value = ''
  pending.value = true
  abortController = new AbortController()
  let receivedDone = false
  try {
    await streamMessage(activeID.value, { content, provider, model }, {
      onMetadata(userMessage, assistantMessage) {
        messages.value = [...messages.value, userMessage, assistantMessage]
        setConversationTitleFromFirstMessage(content)
      },
      onThink(delta) {
        const latest = messages.value[messages.value.length - 1]
        if (latest?.role === 'assistant') latest.think = (latest.think ?? '') + delta
      },
      onDelta(delta) {
        const latest = messages.value[messages.value.length - 1]
        if (latest?.role === 'assistant') latest.content += delta
      },
      onDone(message) {
        receivedDone = true
        replaceMessage(message)
      },
      onError(message) {
        error.value = message
        const latest = messages.value[messages.value.length - 1]
        if (latest?.role === 'assistant') latest.status = 'error'
      }
    }, abortController.signal)
    if (!receivedDone && !error.value && messages.value.at(-1)?.role === 'assistant') {
      messages.value[messages.value.length - 1].status = 'stopped'
    }
  } catch (cause) {
    if (!abortController.signal.aborted) error.value = messageFor(cause)
    const latest = messages.value[messages.value.length - 1]
    if (latest?.role === 'assistant') latest.status = abortController.signal.aborted ? 'stopped' : 'error'
  } finally {
    pending.value = false
    abortController = null
    if (activeID.value) refreshConversationOrder(activeID.value)
  }
}

function stop() { abortController?.abort() }

function replaceMessage(updated: Message) {
  messages.value = messages.value.map((item) => item.id === updated.id ? updated : item)
}

function setConversationTitleFromFirstMessage(content: string) {
  const conversation = activeConversation.value
  if (!conversation || conversation.title !== 'New conversation' || messages.value.length > 2) return
  const title = content.replace(/\s+/g, ' ').slice(0, 36)
  conversation.title = title + (content.length > 36 ? '…' : '')
  void updateTitle(conversation)
}

function refreshConversationOrder(id: string) {
  const conversation = conversations.value.find((item) => item.id === id)
  if (!conversation) return
  conversation.updatedAt = new Date().toISOString()
  conversations.value = [...conversations.value].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
}

function logout() {
  abortController?.abort()
  clearSession()
  user.value = null
  conversations.value = []
  activeID.value = null
  messages.value = []
  providers.value = []
  error.value = ''
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
      @send="send"
      @stop="stop"
    />
  </div>
</template>
