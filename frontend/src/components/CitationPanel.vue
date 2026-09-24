<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { citationLocation, citationSourceAvailable } from '@/lib/knowledge-ui'
import type { MessageCitation } from '@/types'

const props = defineProps<{
  citations: MessageCitation[]
  activeOrdinal: number | null
  phase: 'loading' | 'empty' | 'ready' | 'error'
  error: string
  downloading: boolean
}>()

const emit = defineEmits<{
  close: []
  select: [ordinal: number]
  download: [citation: MessageCitation]
}>()

const dialog = ref<HTMLElement | null>(null)
const ordered = computed(() => [...props.citations].sort((a, b) => a.ordinal - b.ordinal))
const active = computed(() => ordered.value.find((item) => item.ordinal === props.activeOrdinal) ?? ordered.value[0] ?? null)

onMounted(() => dialog.value?.focus())

watch(() => props.activeOrdinal, async () => {
  await nextTick()
  dialog.value?.querySelector<HTMLElement>('.citation-card.active')?.focus()
})

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    emit('close')
    return
  }
  if (!ordered.value.length) return
  const current = ordered.value.findIndex((item) => item.ordinal === (active.value?.ordinal ?? -1))
  if (event.key === 'ArrowDown' || event.key === 'ArrowRight') {
    event.preventDefault()
    const next = ordered.value[Math.min(current + 1, ordered.value.length - 1)]
    if (next) emit('select', next.ordinal)
  }
  if (event.key === 'ArrowUp' || event.key === 'ArrowLeft') {
    event.preventDefault()
    const previous = ordered.value[Math.max(current - 1, 0)]
    if (previous) emit('select', previous.ordinal)
  }
  if (event.key === 'Home') {
    event.preventDefault()
    emit('select', ordered.value[0].ordinal)
  }
  if (event.key === 'End') {
    event.preventDefault()
    emit('select', ordered.value[ordered.value.length - 1].ordinal)
  }
}
</script>

<template>
  <div class="citation-backdrop" @mousedown.self="emit('close')">
    <section
      ref="dialog"
      class="citation-panel"
      role="dialog"
      aria-modal="true"
      aria-labelledby="citation-panel-title"
      tabindex="-1"
      @keydown="onKeydown"
    >
      <header class="citation-panel-header">
        <div>
          <p class="eyebrow">SOURCES</p>
          <h2 id="citation-panel-title">回答依据</h2>
        </div>
        <button class="account-close" type="button" aria-label="关闭来源" title="关闭" @click="emit('close')">
          <svg aria-hidden="true" viewBox="0 0 24 24"><path d="m6 6 12 12M18 6 6 18" /></svg>
        </button>
      </header>

      <div class="citation-panel-body">
        <p v-if="phase === 'loading'" class="citation-state" role="status"><span class="loading-mark" />正在整理这次回答使用的资料…</p>
        <p v-else-if="phase === 'empty'" class="citation-state">这次回答没有引用知识库原文。如果资料还在处理，或所选资料里没有足够依据，回答会直接说明。</p>
        <p v-else-if="phase === 'error'" class="citation-state citation-state-error" role="alert">{{ error || '来源暂时无法显示，请稍后重试。' }}</p>
        <template v-else>
          <div class="citation-ordinals" role="listbox" aria-label="引用编号" aria-orientation="horizontal">
            <button
              v-for="item in ordered"
              :key="item.ordinal"
              type="button"
              role="option"
              class="citation-ordinal"
              :class="{ active: item.ordinal === active?.ordinal }"
              :aria-selected="item.ordinal === active?.ordinal"
              @click="emit('select', item.ordinal)"
            >{{ item.ordinal }}</button>
          </div>
          <article v-if="active" class="citation-card active" tabindex="0">
            <p class="citation-kicker">来源 {{ active.ordinal }}</p>
            <h3>{{ active.sourceName }}</h3>
            <p v-if="citationLocation(active)" class="citation-location">{{ citationLocation(active) }}</p>
            <blockquote>{{ active.quote }}</blockquote>
            <p v-if="!citationSourceAvailable(active)" class="citation-note">原文文件已删除，以下内容是回答当时保存的片段。</p>
            <button
              v-else
              class="secondary-button"
              type="button"
              :disabled="downloading"
              @click="emit('download', active)"
            >{{ downloading ? '正在准备下载…' : '下载原文' }}</button>
            <p v-if="error" class="citation-state-error" role="alert">{{ error }}</p>
          </article>
        </template>
      </div>
    </section>
  </div>
</template>
