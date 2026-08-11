<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import MarkdownContent from '@/components/MarkdownContent.vue'
import { deleteAttachment, downloadAttachment, retryAttachment, uploadAttachment } from '@/lib/api'
import { shouldSendComposerMessage } from '@/lib/composer-keyboard'
import { buildDisplayMessages } from '@/lib/message-tree'
import { isNearScrollBottom } from '@/lib/scroll'
import type { Attachment, Conversation, Message, ModelProvider } from '@/types'

const props = defineProps<{
  conversation: Conversation | null
  messages: Message[]
  providers: ModelProvider[]
  pending: boolean
  error: string
  notice: string
  hasOlderMessages: boolean
  loadingOlderMessages: boolean
  loadingMessages: boolean
}>()

const emit = defineEmits<{
  send: [content: string, provider: string, model: string, attachmentIDs: string[]]
  regenerate: [message: Message, provider: string, model: string]
  editMessage: [message: Message, content: string, provider: string, model: string]
  switchVersion: [message: Message]
  loadOlder: [done: () => void]
  account: []
  stop: []
}>()

const draft = ref('')
const selectedProvider = ref('')
const selectedModel = ref('')
const viewport = ref<HTMLElement | null>(null)
const followLatestMessage = ref(true)
const preservingOlderMessageScroll = ref(false)
const editingID = ref<string | null>(null)
const editDraft = ref('')
const attachments = ref<Attachment[]>([])
const uploading = ref(0)
const attachmentError = ref('')
const draggingFiles = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const modelPicker = ref<HTMLElement | null>(null)
const modelPickerOpen = ref(false)

const models = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.models ?? [])
const providerLabel = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.label ?? '选择模型')
const displayMessages = computed(() => buildDisplayMessages(props.messages))
const selectedProviderConfig = computed(() => props.providers.find((item) => item.id === selectedProvider.value))
const selectedModelSupportsVision = computed(() => selectedProviderConfig.value?.visionModels?.includes(selectedModel.value) ?? false)
const hasImageAttachment = computed(() => attachments.value.some((item) => item.kind === 'image'))
const visionUnsupported = computed(() => hasImageAttachment.value && !selectedProviderConfig.value?.visionModels?.includes(selectedModel.value))
const attachmentBlocked = computed(() => uploading.value > 0 || attachments.value.some((item) => item.status !== 'ready') || visionUnsupported.value)

watch(() => props.providers, (providers) => {
  if (!providers.length) return
  if (!providers.some((item) => item.id === selectedProvider.value)) selectedProvider.value = providers[0].id
}, { immediate: true })
watch(models, (items) => {
  if (!items.includes(selectedModel.value)) selectedModel.value = items[0] || ''
}, { immediate: true })
watch(() => props.pending, (pending) => {
  if (pending) modelPickerOpen.value = false
})
watch(() => props.messages.map((item) => `${item.content}\u0000${item.think ?? ''}`).join('\u0001'), async () => {
  if (!followLatestMessage.value) return
  await nextTick()
  viewport.value?.scrollTo({ top: viewport.value.scrollHeight, behavior: 'auto' })
})

function onViewportScroll() {
  const element = viewport.value
  if (!element) return
  followLatestMessage.value = isNearScrollBottom(element)
  if (element.scrollTop <= 80) requestOlderMessages()
}

function toggleModelPicker() {
  if (props.pending || !props.providers.length) return
  modelPickerOpen.value = !modelPickerOpen.value
}

function chooseModel(provider: ModelProvider, model: string) {
  selectedProvider.value = provider.id
  selectedModel.value = model
  modelPickerOpen.value = false
}

function closeModelPickerOnOutsideClick(event: PointerEvent) {
  if (!modelPicker.value?.contains(event.target as Node)) modelPickerOpen.value = false
}

onMounted(() => document.addEventListener('pointerdown', closeModelPickerOnOutsideClick))
onBeforeUnmount(() => document.removeEventListener('pointerdown', closeModelPickerOnOutsideClick))

function requestOlderMessages() {
  const element = viewport.value
  if (!element || preservingOlderMessageScroll.value || props.loadingOlderMessages || !props.hasOlderMessages) return
  preservingOlderMessageScroll.value = true
  const conversationID = props.conversation?.id
  const previousHeight = element.scrollHeight
  const previousTop = element.scrollTop
  emit('loadOlder', async () => {
    await nextTick()
    const current = viewport.value
    if (current && props.conversation?.id === conversationID) {
      current.scrollTop = previousTop + current.scrollHeight - previousHeight
      preservingOlderMessageScroll.value = false
    }
  })
}

function resumeFollowingLatestMessage() {
  followLatestMessage.value = true
}

function send() {
  const content = draft.value.trim()
  if ((!content && !attachments.value.length) || props.pending || attachmentBlocked.value || !selectedProvider.value || !selectedModel.value) return
  resumeFollowingLatestMessage()
  draft.value = ''
  const attachmentIDs = attachments.value.map((item) => item.id)
  attachments.value = []
  emit('send', content, selectedProvider.value, selectedModel.value, attachmentIDs)
}

function onKeydown(event: KeyboardEvent) {
  if (shouldSendComposerMessage(event)) { event.preventDefault(); send() }
}

function roleName(message: Message) { return message.role === 'user' ? '你' : message.provider ? `${message.provider} · ${message.model}` : '智能助理' }

function regenerate(message: Message) {
  if (props.pending || !selectedProvider.value || !selectedModel.value) return
  resumeFollowingLatestMessage()
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
	resumeFollowingLatestMessage()
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
    resumeFollowingLatestMessage()
    emit('switchVersion', target)
  }
}

watch(() => props.conversation?.id, async (_current, previous) => {
	if (previous) await clearPendingAttachments(previous)
  cancelEditing()
  preservingOlderMessageScroll.value = false
  resumeFollowingLatestMessage()
  await nextTick()
  viewport.value?.scrollTo({ top: viewport.value.scrollHeight, behavior: 'auto' })
})

async function addFiles(files: File[]) {
	const conversationID = props.conversation?.id
	if (!conversationID || props.pending) return
	attachmentError.value = ''
	const remaining = Math.max(0, 5 - attachments.value.length)
	if (files.length > remaining) attachmentError.value = '每条消息最多添加 5 个附件。'
	for (const file of files.slice(0, remaining)) {
		uploading.value++
		try {
			const uploaded = await uploadAttachment(conversationID, file)
			if (props.conversation?.id === conversationID) attachments.value.push(uploaded)
			else await deleteAttachment(conversationID, uploaded.id)
		}
		catch (cause) { attachmentError.value = cause instanceof Error ? cause.message : '文件上传失败' }
		finally { uploading.value-- }
	}
}

function chooseFiles(event: Event) {
	const input = event.target as HTMLInputElement
	void addFiles(Array.from(input.files ?? []))
	input.value = ''
}

function onDrop(event: DragEvent) {
	draggingFiles.value = false
	void addFiles(Array.from(event.dataTransfer?.files ?? []))
}

function onPaste(event: ClipboardEvent) {
	const images = Array.from(event.clipboardData?.files ?? []).filter((file) => file.type.startsWith('image/'))
	if (!images.length) return
	event.preventDefault()
	void addFiles(images)
}

async function removeAttachment(item: Attachment) {
	const conversationID = props.conversation?.id; if (!conversationID) return
	try { await deleteAttachment(conversationID, item.id); attachments.value = attachments.value.filter((candidate) => candidate.id !== item.id) }
	catch (cause) { attachmentError.value = cause instanceof Error ? cause.message : '移除附件失败' }
}

async function retry(item: Attachment) {
	const conversationID = props.conversation?.id; if (!conversationID) return
	attachmentError.value = ''
	try {
		const updated = await retryAttachment(conversationID, item.id)
		attachments.value = attachments.value.map((candidate) => candidate.id === updated.id ? updated : candidate)
	} catch (cause) { attachmentError.value = cause instanceof Error ? cause.message : '重试失败' }
}

async function clearPendingAttachments(conversationID: string) {
	const pendingItems = attachments.value; attachments.value = []; attachmentError.value = ''
	await Promise.allSettled(pendingItems.map((item) => deleteAttachment(conversationID, item.id)))
}

function formatSize(bytes: number) { return bytes < 1024 * 1024 ? `${Math.ceil(bytes / 1024)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB` }
function openAttachment(message: Message, item: Attachment) { void downloadAttachment(message.conversationId, item).catch((cause) => { attachmentError.value = cause instanceof Error ? cause.message : '下载失败' }) }
</script>

<template>
  <main class="chat-main">
    <header class="chat-header">
      <div>
        <p class="eyebrow">CONVERSATION</p>
        <h2>{{ conversation?.title || '选择或新建一个对话' }}</h2>
      </div>
      <div class="chat-header-actions">
        <div class="model-indicator"><span class="status-dot" />{{ providerLabel }} · {{ selectedModel || '未配置' }}</div>
        <button class="mobile-account-button" type="button" aria-label="打开账户与安全设置" title="账户与安全" @click="emit('account')">
          <svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="12" cy="8" r="3.5" /><path d="M5.5 19c.8-3.4 3-5.1 6.5-5.1s5.7 1.7 6.5 5.1" /></svg>
        </button>
      </div>
    </header>

    <div ref="viewport" class="messages" @scroll.passive="onViewportScroll">
      <div v-if="conversation && (hasOlderMessages || loadingOlderMessages)" class="older-messages-control">
        <button type="button" :disabled="loadingOlderMessages || preservingOlderMessageScroll" @click="requestOlderMessages">
          {{ loadingOlderMessages || preservingOlderMessageScroll ? '正在加载更早消息…' : '加载更早消息' }}
        </button>
      </div>
      <section v-if="loadingMessages" class="messages-loading" role="status"><span />正在加载对话…</section>
      <section v-else-if="!conversation" class="welcome-state">
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
      <article v-for="entry in displayMessages" :key="entry.message.id" class="message" :class="entry.message.role">
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
		<div v-if="entry.message.attachments?.length" class="message-attachments" aria-label="消息附件">
			<button v-for="item in entry.message.attachments" :key="item.id" type="button" @click="openAttachment(entry.message, item)">
				<svg aria-hidden="true" viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/></svg>
				<span>{{ item.name }}</span><small>{{ formatSize(item.size) }}</small>
			</button>
		</div>
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
      <div class="composer" :class="{ disabled: !conversation || !providers.length, 'is-dragging': draggingFiles }" @dragenter.prevent="draggingFiles = true" @dragover.prevent @dragleave.self="draggingFiles = false" @drop.prevent="onDrop">
		<div v-if="draggingFiles" class="drop-overlay">松开即可添加文件</div>
		<div v-if="attachments.length || uploading" class="attachment-list" aria-live="polite">
			<div v-for="item in attachments" :key="item.id" class="attachment-chip" :class="item.status">
				<div><strong>{{ item.name }}</strong><span>{{ item.status === 'failed' ? item.errorMessage : `${formatSize(item.size)} · ${item.kind === 'image' ? '图片' : '文档'}` }}</span></div>
				<button v-if="item.status === 'failed'" type="button" aria-label="重试解析" @click="retry(item)">重试</button>
				<button type="button" :aria-label="`移除 ${item.name}`" @click="removeAttachment(item)">×</button>
			</div>
			<div v-if="uploading" class="attachment-chip uploading"><span class="attachment-spinner" />正在安全上传并解析…</div>
		</div>
		<p v-if="visionUnsupported" class="attachment-inline-error" role="alert">当前模型不支持图片输入，请切换到标记为视觉能力的模型。</p>
		<p v-if="attachmentError" class="attachment-inline-error" role="alert">{{ attachmentError }}</p>
        <textarea v-model="draft" :disabled="!conversation || !providers.length || pending" placeholder="给 Agent 一个任务…" rows="1" @keydown="onKeydown" @paste="onPaste" />
        <div class="composer-bottom">
		  <div class="composer-tools">
			<input ref="fileInput" class="visually-hidden" type="file" multiple accept=".png,.jpg,.jpeg,.gif,.webp,.txt,.pdf,.docx" @change="chooseFiles">
			<button class="attach-button" type="button" :disabled="!conversation || pending || attachments.length >= 5" aria-label="添加图片或文件" title="添加图片或文件" @click="fileInput?.click()">
				<svg aria-hidden="true" viewBox="0 0 24 24"><path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg>
			</button>
		  <div ref="modelPicker" class="model-picker" @keydown.esc="modelPickerOpen = false">
			<button
			  class="model-picker-trigger"
			  type="button"
			  :disabled="pending || !providers.length"
			  aria-haspopup="listbox"
			  :aria-expanded="modelPickerOpen"
			  aria-label="切换模型"
			  @click="toggleModelPicker"
			>
			  <span class="model-picker-mark" aria-hidden="true"><span /></span>
			  <span class="model-picker-current">
				<small>{{ providerLabel }}</small>
				<strong>{{ selectedModel || '选择模型' }}</strong>
			  </span>
			  <span v-if="selectedModelSupportsVision" class="model-capability">视觉</span>
			  <svg class="model-picker-chevron" :class="{ open: modelPickerOpen }" aria-hidden="true" viewBox="0 0 20 20"><path d="m6 8 4 4 4-4" /></svg>
			</button>
			<div v-if="modelPickerOpen" class="model-picker-menu" role="listbox" aria-label="可用模型">
			  <div class="model-picker-heading"><strong>选择模型</strong><span>{{ providers.length }} 个供应商</span></div>
			  <div v-for="provider in providers" :key="provider.id" class="model-provider-group" role="group" :aria-label="provider.label">
				<p>{{ provider.label }}</p>
				<button
				  v-for="model in provider.models"
				  :key="`${provider.id}:${model}`"
				  type="button"
				  role="option"
				  :aria-selected="provider.id === selectedProvider && model === selectedModel"
				  :class="{ selected: provider.id === selectedProvider && model === selectedModel }"
				  @click="chooseModel(provider, model)"
				>
				  <span class="model-option-check" aria-hidden="true"><svg viewBox="0 0 20 20"><path d="m5 10 3 3 7-7" /></svg></span>
				  <span class="model-option-copy"><strong>{{ model }}</strong><small>{{ provider.label }}</small></span>
				  <span v-if="provider.visionModels?.includes(model)" class="model-capability">视觉</span>
				</button>
			  </div>
			</div>
		  </div></div>
          <button v-if="pending" class="stop-button" @click="emit('stop')"><span />停止生成</button>
          <button v-else class="send-button" :disabled="(!draft.trim() && !attachments.length) || attachmentBlocked || !conversation || !selectedModel" @click="send">发送</button>
        </div>
      </div>
      <p class="composer-hint">Enter 发送 · Shift + Enter 换行 · 对话内容将保存在你的工作空间</p>
    </footer>
  </main>
</template>
