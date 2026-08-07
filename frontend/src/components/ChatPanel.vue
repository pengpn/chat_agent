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
  editMessage: [message: Message, content: string, provider: string, model: string]
  switchVersion: [message: Message]
  stop: []
}>()

const draft = ref('')
const selectedProvider = ref('')
const selectedModel = ref('')
const viewport = ref<HTMLElement | null>(null)
const editingID = ref<string | null>(null)
const editDraft = ref('')

const models = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.models ?? [])
const providerLabel = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.label ?? '选择模型')
const displayMessages = computed(() => {
	if (!props.messages.some((message) => message.parentMessageId || message.isActiveChild)) return legacyDisplayMessages(props.messages)
	const children = new Map<string, Message[]>()
	for (const message of props.messages) {
		const parentID = message.parentMessageId ?? ''
		const siblings = children.get(parentID) ?? []
		siblings.push(message)
		children.set(parentID, siblings)
	}
	for (const siblings of children.values()) siblings.sort((a, b) => a.createdAt.localeCompare(b.createdAt))

	const entries: Array<{ message: Message; versions: Message[] }> = []
	const visited = new Set<string>()
	let parentID = ''
	while (true) {
		const siblings = children.get(parentID) ?? []
		const active = [...siblings].reverse().find((message) => message.isActiveChild)
		if (!active || visited.has(active.id)) break
		visited.add(active.id)
		entries.push({ message: active, versions: siblings })
		parentID = active.id
	}
	return entries
})

function legacyDisplayMessages(messages: Message[]) {
  const groups = new Map<string, Message[]>()
  for (const message of messages) {
    if (message.role !== 'assistant' || !message.replyToMessageId) continue
    const versions = groups.get(message.replyToMessageId) ?? []
    versions.push(message)
    groups.set(message.replyToMessageId, versions)
  }
  for (const versions of groups.values()) versions.sort((a, b) => (a.responseVersion || 1) - (b.responseVersion || 1))

  const seen = new Set<string>()
  return messages.flatMap((message) => {
    if (message.role !== 'assistant' || !message.replyToMessageId) return [{ message, versions: [message] }]
    if (seen.has(message.replyToMessageId)) return []
    seen.add(message.replyToMessageId)
    const versions = groups.get(message.replyToMessageId) ?? [message]
    const active = [...versions].reverse().find((item) => item.isActiveResponse) ?? versions.at(-1) ?? message
    return [{ message: active, versions }]
  })
}

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

function startEditing(message: Message) {
	if (props.pending) return
	editingID.value = message.id
	editDraft.value = message.content
	nextTick(() => document.getElementById(`edit-${message.id}`)?.focus())
}

function cancelEditing() {
	editingID.value = null
	editDraft.value = ''
}

function submitEdit(message: Message) {
	const content = editDraft.value.trim()
	if (!content || content === message.content || props.pending || !selectedProvider.value || !selectedModel.value) return
	emit('editMessage', message, content, selectedProvider.value, selectedModel.value)
	cancelEditing()
}

function onEditKeydown(event: KeyboardEvent, message: Message) {
	if (event.key === 'Escape') {
		event.preventDefault()
		cancelEditing()
	}
	if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
		event.preventDefault()
		submitEdit(message)
	}
}

function switchVersion(versions: Message[], current: Message, offset: number) {
  if (props.pending) return
  const index = versions.findIndex((item) => item.id === current.id)
  const target = versions[index + offset]
  if (target) {
    cancelEditing()
    emit('switchVersion', target)
  }
}

watch(() => props.conversation?.id, cancelEditing)
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
      <article v-for="entry in displayMessages" :key="`${entry.message.role}:${entry.message.parentMessageId || 'root'}`" class="message" :class="entry.message.role">
        <div class="message-meta"><span class="message-avatar" :class="{ agent: entry.message.role !== 'user' }">{{ entry.message.role === 'user' ? '你' : 'a' }}</span>{{ roleName(entry.message) }}</div>
		<div v-if="entry.message.role === 'user' && editingID === entry.message.id" class="message-edit-form">
			<label :for="`edit-${entry.message.id}`">编辑消息</label>
			<textarea :id="`edit-${entry.message.id}`" v-model="editDraft" rows="3" maxlength="100000" @keydown="onEditKeydown($event, entry.message)" />
			<div class="message-edit-footer">
				<span>⌘ / Ctrl + Enter 保存 · Esc 取消</span>
				<div><button class="secondary-action" @click="cancelEditing">取消</button><button class="primary-action" :disabled="pending || !editDraft.trim() || editDraft.trim() === entry.message.content" @click="submitEdit(entry.message)">保存并生成</button></div>
			</div>
		</div>
		<template v-else>
        <details v-if="entry.message.think" class="message-think" :open="entry.message.status === 'streaming'">
          <summary>思考过程</summary>
          <div class="message-think-content">{{ entry.message.think }}</div>
        </details>
		<pre v-if="entry.message.structuredOutput !== undefined && entry.message.structuredOutput !== null" class="structured-output"><code>{{ JSON.stringify(entry.message.structuredOutput, null, 2) }}</code></pre>
		<MarkdownContent v-else :content="entry.message.content" /><span v-if="entry.message.status === 'streaming'" class="cursor" />
		<p v-if="entry.message.status === 'interrupted'" class="message-status">回答已中断，可重新生成。</p>
        <p v-if="entry.message.status === 'error'" class="message-status">这次回答未能完成，请重试。</p>
        <div class="response-actions">
          <div v-if="entry.versions.length > 1" class="response-version" role="group" :aria-label="entry.message.role === 'user' ? '问题分支切换' : '回答版本切换'">
            <button :disabled="pending || entry.message.id === entry.versions[0]?.id" :aria-label="entry.message.role === 'user' ? '上一个问题分支' : '上一个回答版本'" @click="switchVersion(entry.versions, entry.message, -1)">‹</button>
            <span aria-live="polite">{{ entry.versions.findIndex((item) => item.id === entry.message.id) + 1 }} / {{ entry.versions.length }}</span>
            <button :disabled="pending || entry.message.id === entry.versions.at(-1)?.id" :aria-label="entry.message.role === 'user' ? '下一个问题分支' : '下一个回答版本'" @click="switchVersion(entry.versions, entry.message, 1)">›</button>
          </div>
		  <button v-if="entry.message.role === 'user'" class="regenerate-button" :disabled="pending" @click="startEditing(entry.message)">
			<svg aria-hidden="true" viewBox="0 0 24 24"><path d="m4 20 4.5-1 10-10a2.12 2.12 0 0 0-3-3l-10 10L4 20Z" /><path d="m13.5 7.5 3 3" /></svg>编辑
		  </button>
          <button v-else class="regenerate-button" :disabled="pending || entry.message.status === 'streaming'" @click="regenerate(entry.message)">
            <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" /></svg>重新生成
          </button>
        </div>
		</template>
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
