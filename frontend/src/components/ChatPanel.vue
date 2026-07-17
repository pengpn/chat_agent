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
}>()

const emit = defineEmits<{
  send: [content: string, provider: string, model: string]
  stop: []
}>()

const draft = ref('')
const selectedProvider = ref('')
const selectedModel = ref('')
const viewport = ref<HTMLElement | null>(null)

const models = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.models ?? [])
const providerLabel = computed(() => props.providers.find((item) => item.id === selectedProvider.value)?.label ?? '选择模型')

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
      <article v-for="message in messages" :key="message.id" class="message" :class="message.role">
        <div class="message-meta"><span class="message-avatar" :class="{ agent: message.role !== 'user' }">{{ message.role === 'user' ? '你' : 'a' }}</span>{{ roleName(message) }}</div>
        <details v-if="message.think" class="message-think" :open="message.status === 'streaming'">
          <summary>思考过程</summary>
          <div class="message-think-content">{{ message.think }}</div>
        </details>
        <MarkdownContent :content="message.content" /><span v-if="message.status === 'streaming'" class="cursor" />
        <p v-if="message.status === 'error'" class="message-status">这次回答未能完成，请重试。</p>
      </article>
    </div>

    <footer class="composer-wrap">
      <p v-if="error" class="stream-error">{{ error }}</p>
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
