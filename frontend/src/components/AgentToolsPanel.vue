<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { getAgentToolState, readAgentArtifact, respondToolInteraction } from '@/lib/api'
import type { AgentArtifact, AgentToolState, ToolInteraction } from '@/lib/api'
import { formatInteractionCountdown, remainingInteractionSeconds, TOOL_FEEDBACK_DURATION_MS } from '@/lib/tool-interaction'

const props = defineProps<{ conversationID: string; revision: number; pending: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const state = ref<AgentToolState>({ plan: null, interactions: [], artifacts: [] })
const answers = ref<Record<string, string>>({})
const busy = ref('')
const error = ref('')
const loadError = ref('')
const feedback = ref('')
const now = ref(Date.now())
const previews = ref<Record<string, AgentArtifact>>({})
const statusLabels = { pending: '待处理', in_progress: '进行中', completed: '已完成' }
let refresh: (() => Promise<void>) | undefined
let feedbackTimer: ReturnType<typeof setTimeout> | undefined
let scopeVersion = 0
const dismissedIDs = new Set<string>()

function clearFeedback() {
  if (feedbackTimer !== undefined) clearTimeout(feedbackTimer)
  feedbackTimer = undefined
  feedback.value = ''
}

function showFeedback(message: string) {
  clearFeedback()
  feedback.value = message
  feedbackTimer = setTimeout(clearFeedback, TOOL_FEEDBACK_DURATION_MS)
}

function retireExpiredInteractions() {
  const expired = state.value.interactions.filter((item) => item.id !== busy.value && remainingInteractionSeconds(item.expiresAt, now.value) === 0)
  if (!expired.length) return
  for (const item of expired) { dismissedIDs.add(item.id); delete answers.value[item.id] }
  state.value.interactions = state.value.interactions.filter((item) => !dismissedIDs.has(item.id))
  showFeedback(expired.some((item) => item.kind === 'approval') ? '审批等待已到期，请查看任务的最新状态。' : '答复等待已到期，请查看任务的最新状态。')
  void refresh?.()
}

// 按真实截止时间重算，不使用每次减一；后台恢复后不会显示过期的剩余时长。
watch(() => state.value.interactions.length, (count, _previous, onCleanup) => {
  if (!count) return
  const tick = () => { now.value = Date.now(); retireExpiredInteractions() }
  tick()
  const timer = setInterval(tick, 1000)
  window.addEventListener('focus', tick)
  document.addEventListener('visibilitychange', tick)
  onCleanup(() => { clearInterval(timer); window.removeEventListener('focus', tick); document.removeEventListener('visibilitychange', tick) })
})

// 显式监听会话，清理旧请求和轮询；避免切换会话后显示上一会话的审批。
watch(() => props.conversationID, (id, _old, onCleanup) => {
  scopeVersion++
  const controller = new AbortController()
  let loading = false
  state.value = { plan: null, interactions: [], artifacts: [] }
  dismissedIDs.clear(); clearFeedback()
  answers.value = {}; previews.value = {}; error.value = ''; loadError.value = ''; busy.value = ''; now.value = Date.now()
  const load = async () => {
    if (loading || controller.signal.aborted) return
    loading = true
    try {
      const result = await getAgentToolState(id, controller.signal)
      if (controller.signal.aborted) return
      // 成功提交或本地到期的交互，不能被较早发出的 GET 响应带回页面。
      const next = { ...result, interactions: result.interactions.filter((item) => !dismissedIDs.has(item.id)) }
      const changed = JSON.stringify(next) !== JSON.stringify(state.value)
      state.value = next
      now.value = Date.now()
      retireExpiredInteractions()
      loadError.value = ''
      if (changed) { await nextTick(); emit('changed') }
    } catch (cause) {
      if (!controller.signal.aborted) loadError.value = cause instanceof Error ? cause.message : '工具状态加载失败，请重试'
    } finally { loading = false }
  }
  refresh = load
  void load()
  // SSE 是主要通知，生成期间轮询用于事件丢失或重连后的兜底。
  const timer = setInterval(() => { if (props.pending || state.value.interactions.length) void load() }, 2000)
  onCleanup(() => { scopeVersion++; controller.abort(); clearInterval(timer); clearFeedback(); if (refresh === load) refresh = undefined })
}, { immediate: true })
watch(() => props.revision, () => { void refresh?.() })
watch(() => props.pending, () => { clearFeedback(); void refresh?.() })

async function respond(item: ToolInteraction, approved?: boolean) {
  if (busy.value || dismissedIDs.has(item.id)) return
  now.value = Date.now()
  if (remainingInteractionSeconds(item.expiresAt, now.value) === 0) { retireExpiredInteractions(); return }
  const id = props.conversationID
  const version = scopeVersion
  clearFeedback()
  busy.value = item.id; error.value = ''
  try {
    await respondToolInteraction(id, item.id, item.kind === 'approval' ? { approved } : { answer: answers.value[item.id]?.trim() })
    if (version !== scopeVersion) return
    dismissedIDs.add(item.id)
    delete answers.value[item.id]
    state.value.interactions = state.value.interactions.filter((entry) => entry.id !== item.id)
    showFeedback(item.kind === 'question' ? '答复已提交，Agent 将继续处理。' : approved ? '已批准，仅允许这组参数执行一次。' : '已拒绝，本次命令不会执行。')
    await refresh?.()
  } catch (cause) {
    if (version === scopeVersion) error.value = cause instanceof Error ? cause.message : '答复未提交，请重试'
  } finally { if (version === scopeVersion) { busy.value = ''; now.value = Date.now(); retireExpiredInteractions() } }
}
async function preview(item: AgentArtifact, download = false) {
  const id = props.conversationID
  const version = scopeVersion
  busy.value = item.id; error.value = ''
  try {
    const result = await readAgentArtifact(id, item.id)
    if (version !== scopeVersion) return
    previews.value[item.id] = result
    if (download) {
      const extension = result.mediaType === 'application/json' ? '.json' : result.mediaType === 'text/markdown' ? '.md' : '.txt'
      const url = URL.createObjectURL(new Blob([result.content ?? ''], { type: 'text/plain;charset=utf-8' }))
      const link = document.createElement('a')
      link.href = url; link.download = result.title.replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_') + extension
      link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
    }
  } catch (cause) {
    if (version === scopeVersion) error.value = cause instanceof Error ? cause.message : '读取产物失败，请重试'
  } finally { if (version === scopeVersion) busy.value = '' }
}
</script>

<template>
  <section v-if="state.plan || state.interactions.length || state.artifacts.length || error || loadError || feedback" class="agent-tools" aria-label="任务进度与工具交互">
    <details v-if="state.plan" class="tool-card" open>
      <summary>任务计划 <span class="tool-muted">第 {{ state.plan.revision }} 版</span></summary>
      <ol><li v-for="(step, index) in state.plan.steps" :key="index"><span>{{ step.title }}</span><small>{{ statusLabels[step.status] }}</small></li></ol>
    </details>
    <details v-if="state.artifacts.length" class="tool-card" open>
      <summary>任务产物 <span class="tool-muted">{{ state.artifacts.length }} 项</span></summary>
      <article v-for="item in state.artifacts" :key="item.id" class="artifact-row">
        <strong>{{ item.title }}</strong><small class="tool-muted">{{ item.mediaType }} · {{ Math.ceil(item.size / 1024) }} KB</small>
        <div class="tool-actions"><button type="button" :disabled="!!busy" @click="previews[item.id] ? delete previews[item.id] : preview(item)">{{ previews[item.id] ? '收起' : '查看内容' }}</button><button type="button" :disabled="!!busy" @click="preview(item, true)">下载</button></div>
        <pre v-if="previews[item.id]">{{ previews[item.id]?.content }}</pre>
      </article>
    </details>
    <!-- 待处理交互放在末尾，跟随最新输出时不会被历史产物挤到屏幕外。 -->
    <section v-for="item in state.interactions" :key="item.id" class="tool-card interaction-card">
      <template v-if="item.kind === 'question'">
        <h3>需要你的补充</h3>
        <form @submit.prevent="respond(item)">
          <label :for="`tool-answer-${item.id}`">{{ item.request.question }}</label>
          <div v-if="item.request.options?.length" class="tool-actions" aria-label="建议答复">
            <button v-for="option in item.request.options" :key="option" type="button" :disabled="!!busy" @click="answers[item.id] = option">{{ option }}</button>
          </div>
          <textarea :id="`tool-answer-${item.id}`" v-model="answers[item.id]" rows="3" maxlength="8000" :disabled="!!busy" required placeholder="选择上方选项，或输入你的答复" />
          <div class="tool-actions"><button class="primary" :disabled="!!busy || !answers[item.id]?.trim()">{{ busy === item.id ? '正在提交…' : '提交答复' }}</button></div>
        </form>
      </template>
      <template v-else>
        <h3>执行前需要你的批准</h3>
        <p>{{ item.request.reason }}</p>
        <p class="tool-muted">{{ item.request.descriptor?.namespace }}.{{ item.request.descriptor?.name }} · v{{ item.request.descriptor?.version }}</p>
        <pre>{{ JSON.stringify(item.request.arguments, null, 2) }}</pre>
        <p class="tool-muted">仅授权本轮任务的这组参数一次；操作仍在隔离沙箱中执行。拒绝不会执行命令。</p>
        <div class="tool-actions">
          <button type="button" :disabled="!!busy" @click="respond(item, false)">拒绝</button>
          <button type="button" class="primary" :disabled="!!busy" @click="respond(item, true)">{{ busy === item.id ? '正在提交…' : '仅允许这一次' }}</button>
        </div>
      </template>
      <p class="tool-muted interaction-expiry">
        <span class="interaction-countdown" role="timer" aria-live="off" :title="`截止时间：${new Date(item.expiresAt).toLocaleTimeString()}`">{{ remainingInteractionSeconds(item.expiresAt, now) === 0 && busy === item.id ? '正在确认处理结果…' : formatInteractionCountdown(remainingInteractionSeconds(item.expiresAt, now)) }}</span>
        <span>停止生成会取消等待。</span>
      </p>
    </section>
    <p v-if="feedback" class="tool-muted" role="status">{{ feedback }}</p>
    <div v-if="error || loadError" class="tool-card"><p class="tool-error" role="alert">{{ error || loadError }}</p><button type="button" :disabled="!!busy" @click="error = ''; refresh?.()">刷新工具状态</button></div>
  </section>
</template>

<style scoped>
.agent-tools { width: 100%; max-width: var(--content-max-width); min-width: 0; margin: var(--space-4) auto; display: grid; gap: var(--space-3); font-size: var(--font-size-body); }
.tool-card { min-width: 0; padding: var(--space-4); border: 1px solid var(--color-border); border-radius: var(--radius-md); background: var(--color-surface); overflow-wrap: anywhere; }
.interaction-card { border-color: var(--color-border-strong); background: var(--color-primary-soft); }
h3, summary { font-size: var(--font-size-body); font-weight: 600; color: var(--color-text-primary); }
h3 { margin: 0 0 var(--space-3); }
p { margin: var(--space-2) 0; }
form { display: grid; gap: var(--space-3); }
label { line-height: 1.6; }
.tool-muted, small { color: var(--color-text-secondary); font-size: var(--font-size-body-sm); font-weight: 400; }
summary { cursor: pointer; min-height: var(--touch-target); align-content: center; }
summary span { margin-left: var(--space-2); }
.tool-actions { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-top: var(--space-2); }
button { min-height: var(--touch-target); min-width: var(--touch-target); padding: var(--space-2) var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-surface-raised); color: var(--color-text-secondary); font: inherit; cursor: pointer; overflow-wrap: anywhere; }
button:hover:not(:disabled) { background: var(--color-surface-muted); border-color: var(--color-border-strong); }
button.primary { background: var(--color-primary); border-color: var(--color-primary); color: var(--color-on-primary); }
button.primary:hover:not(:disabled) { background: var(--color-primary-hover); }
button:disabled, textarea:disabled { opacity: .5; cursor: not-allowed; }
button:focus-visible, summary:focus-visible, textarea:focus-visible { outline: 2px solid var(--color-focus-ring); outline-offset: 2px; }
textarea { display: block; width: 100%; min-width: 0; box-sizing: border-box; padding: var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-surface-raised); color: var(--color-text-primary); font: inherit; resize: vertical; }
pre { max-width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; padding: var(--space-3); background: var(--color-surface-muted); border: 1px solid var(--color-border-subtle); border-radius: var(--radius-sm); font: var(--font-size-body-sm)/1.7 var(--font-mono); }
ol { margin: var(--space-2) 0; padding-left: var(--space-5); }
li { padding: var(--space-2) 0; }
li small { margin-left: var(--space-3); white-space: nowrap; }
.artifact-row { border-top: 1px solid var(--color-border-subtle); padding-top: var(--space-3); margin-top: var(--space-3); }
.artifact-row strong, .artifact-row > small { display: block; }
.tool-error { color: var(--color-danger); }
.interaction-expiry { display: flex; flex-wrap: wrap; gap: var(--space-2) var(--space-3); }
.interaction-countdown { font-variant-numeric: tabular-nums; font-family: var(--font-mono); }
@media (max-width: 760px) { textarea { font-size: var(--font-size-input-mobile); } .tool-card { padding: var(--space-3); } }
</style>
