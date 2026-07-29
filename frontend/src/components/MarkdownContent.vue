<script setup lang="ts">
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { computed, onBeforeUnmount } from 'vue'

const props = defineProps<{ content: string }>()
const feedbackTimers = new Map<HTMLButtonElement, number>()

const renderedContent = computed(() => {
  const html = marked.parse(props.content, { async: false, breaks: true, gfm: true })
  const sanitized = DOMPurify.sanitize(html, {
    FORBID_ATTR: ['style'],
    FORBID_TAGS: ['embed', 'iframe', 'object', 'script', 'style'],
    USE_PROFILES: { html: true },
  })
  const template = document.createElement('template')
  template.innerHTML = sanitized
  for (const pre of template.content.querySelectorAll('pre')) {
    const wrapper = document.createElement('div')
    wrapper.className = 'code-block'
    const toolbar = document.createElement('div')
    toolbar.className = 'code-block-toolbar'
    const language = pre.querySelector('code')?.className.match(/(?:^|\s)language-([^\s]+)/)?.[1]
    const languageLabel = document.createElement('span')
    languageLabel.className = 'code-block-language'
    languageLabel.textContent = language || '代码'
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'code-copy-button'
    button.dataset.copyCode = ''
    button.setAttribute('aria-label', '复制代码')
    button.setAttribute('aria-live', 'polite')
    button.textContent = '复制'
    toolbar.append(languageLabel, button)
    pre.before(wrapper)
    wrapper.append(toolbar, pre)
  }
  return template.innerHTML
})

async function copyCode(event: MouseEvent) {
  const target = event.target as HTMLElement | null
  const button = target?.closest<HTMLButtonElement>('[data-copy-code]')
  if (!button) return
  const code = button.closest('.code-block')?.querySelector('code')?.textContent
  if (code == null) return

  const copied = await writeClipboard(code)
  const previousTimer = feedbackTimers.get(button)
  if (previousTimer !== undefined) window.clearTimeout(previousTimer)
  button.textContent = copied ? '已复制' : '复制失败'
  button.classList.toggle('copied', copied)
  button.classList.toggle('copy-failed', !copied)
  const timer = window.setTimeout(() => {
    button.textContent = '复制'
    button.classList.remove('copied', 'copy-failed')
    feedbackTimers.delete(button)
  }, 1500)
  feedbackTimers.set(button, timer)
}

async function writeClipboard(content: string): Promise<boolean> {
  let textarea: HTMLTextAreaElement | null = null
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(content)
      return true
    }
    textarea = document.createElement('textarea')
    textarea.value = content
    textarea.setAttribute('readonly', '')
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    const copied = document.execCommand('copy')
    return copied
  } catch {
    return false
  } finally {
    textarea?.remove()
  }
}

onBeforeUnmount(() => {
  for (const timer of feedbackTimers.values()) window.clearTimeout(timer)
  feedbackTimers.clear()
})
</script>

<template>
  <div class="message-content markdown-content" @click="copyCode" v-html="renderedContent" />
</template>
