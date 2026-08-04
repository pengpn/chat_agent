<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import MarkdownContent from '@/components/MarkdownContent.vue'
import type { Conversation, Message, ModelProvider } from '@/types'

const props = defineProps<{
  conversation: Conversation | null
  messages: Message[]
  providers: ModelProvider[]
  pending: boolean
  error: string
  notice: string
}>()

const emit = defineEmits<{
  send: [content: string, provider: string, model: string]
  regenerate: [message: Message, provider: string, model: string]
  switchVersion: [message: Message]
  stop: []
}>()

const draft = ref('')
const selectedProvider = ref('')
const selectedModel = ref('')
const viewport = ref<HTMLElement | null>(null)

const models = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.models ?? [])
const providerLabel = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.label ?? '选择模型')
const displayMessages = computed(() => {
  const groups = new Map<string, Message[]>()
  for (const message of props.messages) {
    if (message.role !== 'assistant' || !message.replyToMessageId) continue
    const versions = groups.get(message.replyToMessageId) ?? []
    versions.push(message)
    groups.set(message.replyToMessageId, versions)
  }
  for (const versions of groups.values()) versions.sort((a, b) => (a.responseVersion || 1) - (b.responseVersion || 1))

  const seen = new Set<string>()
  return props.messages.flatMap((message) => {
    if (message.role !== 'assistant' || !message.replyToMessageId) return [{ message, versions: [message] }]
    if (seen.has(message.replyToMessageId)) return []
    seen.add(message.replyToMessageId)
    const versions = groups.get(message.replyToMessageId) ?? [message]
    const active = [...versions].reverse().find((item) => item.isActiveResponse) ?? versions.at(-1) ?? message
    return [{ message: active, versions }]
  })
})

watch(() => props.providers, (providers) => {
  if (!providers.length) return
  if (!providers.some((item) => item.id === selectedProvider.value)) selectedProvider.value = providers[0].id
}, { immediate: true })
watch(models, (items) => {
  if (!items.includes(selectedModel.value)) selectedModel.value = items[0] || ''
}, { immediate: true })
watch(() => props.messages.map((item) => `${item.content}\u0000${item.think ?? ''}`).join('\u0001'), async () => {
  await nextTick()
  viewport.value?.scrollTo({ top: viewport.value.scrollHeight, behavior: 'smooth' })
})

function send() {
  const content = draft.value.trim()
  if (!content || props.pending || !selectedProvider.value || !selectedModel.value) return
  draft.value = ''
  emit('send', content, selectedProvider.value, selectedModel.value)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); send() }
}

function roleName(message: Message) { return message.role === 'user' ? '你' : message.provider ? `${message.provider} · ${message.model}` : '智能助理' }

function regenerate(message: Message) {
  if (props.pending || !selectedProvider.value || !selectedModel.value) return
  emit('regenerate', message, selectedProvider.value, selectedModel.value)
}

function switchVersion(versions: Message[], current: Message, offset: number) {
  if (props.pending) return
  const index = versions.findIndex((item) => item.id === current.id)
  const target = versions[index + offset]
  if (target) emit('switchVersion', target)
}
</script>

<template>
  <main class="chat-main">
    <header class="chat-header">
      <div>
        <p class="eyebrow">CONVERSATION</p>
        <h2>{{ conversation?.title || '选择或新建一个对话' }}</h2>
      </div>
      <div class="model-indicator"><span class="status-dot" />{{ providerLabel }} · {{ selectedModel || '未配置' }}</div>
    </header>

    <div ref="viewport" class="messages">
      <section v-if="!conversation" class="welcome-state">
        <div class="welcome-mark" aria-hidden="true"><i /><i /><i /><i /></div>
        <p class="eyebrow">START A NEW CONVERSATION</p>
        <h1>从一段对话开始工作</h1>
        <p>提出问题、整理思路或交办任务，让智能助理陪你推进下一步。</p>
      </section>
      <section v-else-if="!messages.length" class="welcome-state compact">
        <div class="welcome-mark compact-mark" aria-hidden="true"><i /><i /><i /><i /></div>
        <h1>从一个问题开始</h1>
        <p>你的每一段对话都会保留上下文，方便随时继续。</p>
      </section>
      <article v-for="entry in displayMessages" :key="entry.message.replyToMessageId || entry.message.id" class="message" :class="entry.message.role">
        <div class="message-meta"><span class="message-avatar" :class="{ agent: entry.message.role !== 'user' }">{{ entry.message.role === 'user' ? '你' : 'a' }}</span>{{ roleName(entry.message) }}</div>
        <details v-if="entry.message.think" class="message-think" :open="entry.message.status === 'streaming'">
          <summary>思考过程</summary>
          <div class="message-think-content">{{ entry.message.think }}</div>
        </details>
		<pre v-if="entry.message.structuredOutput !== undefined && entry.message.structuredOutput !== null" class="structured-output"><code>{{ JSON.stringify(entry.message.structuredOutput, null, 2) }}</code></pre>
		<MarkdownContent v-else :content="entry.message.content" /><span v-if="entry.message.status === 'streaming'" class="cursor" />
		<p v-if="entry.message.status === 'interrupted'" class="message-status">回答已中断，可重新生成。</p>
        <p v-if="entry.message.status === 'error'" class="message-status">这次回答未能完成，请重试。</p>
        <div v-if="entry.message.role === 'assistant'" class="response-actions">
          <div v-if="entry.versions.length > 1" class="response-version" aria-label="回答版本切换">
            <button :disabled="pending || entry.message.id === entry.versions[0]?.id" aria-label="上一个回答版本" @click="switchVersion(entry.versions, entry.message, -1)">‹</button>
            <span aria-live="polite">{{ entry.versions.findIndex((item) => item.id === entry.message.id) + 1 }} / {{ entry.versions.length }}</span>
            <button :disabled="pending || entry.message.id === entry.versions.at(-1)?.id" aria-label="下一个回答版本" @click="switchVersion(entry.versions, entry.message, 1)">›</button>
          </div>
          <button class="regenerate-button" :disabled="pending || entry.message.status === 'streaming'" @click="regenerate(entry.message)">
            <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" /></svg>重新生成
          </button>
        </div>
      </article>
    </div>

    <footer class="composer-wrap">
      <p v-if="notice" class="stream-notice" role="status" aria-live="polite">{{ notice }}</p>
      <p v-if="error" class="stream-error" role="alert">{{ error }}</p>
      <div v-if="!providers.length" class="configuration-notice">当前工作空间暂未配置可用模型，请联系工作空间管理员。</div>
      <div class="composer" :class="{ disabled: !conversation || !providers.length }">
        <textarea v-model="draft" :disabled="!conversation || !providers.length || pending" placeholder="给 Agent 一个任务…" rows="1" @keydown="onKeydown" />
        <div class="composer-bottom">
          <div class="model-selects">
            <select v-model="selectedProvider" :disabled="pending || !providers.length" aria-label="Provider">
              <option v-for="provider in providers" :key="provider.id" :value="provider.id">{{ provider.label }}</option>
            </select>
            <select v-model="selectedModel" :disabled="pending || !models.length" aria-label="模型">
              <option v-for="model in models" :key="model" :value="model">{{ model }}</option>
            </select>
          </div>
          <button v-if="pending" class="stop-button" @click="emit('stop')"><span />停止生成</button>
          <button v-else class="send-button" :disabled="!draft.trim() || !conversation || !selectedModel" @click="send">发送</button>
        </div>
      </div>
      <p class="composer-hint">Enter 发送 · Shift + Enter 换行 · 对话内容将保存在你的工作空间</p>
    </footer>
  </main>
</template>
