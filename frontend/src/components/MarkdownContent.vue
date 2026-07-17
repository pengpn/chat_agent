<script setup lang="ts">
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { computed } from 'vue'

const props = defineProps<{ content: string }>()

const renderedContent = computed(() => {
  const html = marked.parse(props.content, { async: false, breaks: true, gfm: true })
  return DOMPurify.sanitize(html, {
    FORBID_ATTR: ['style'],
    FORBID_TAGS: ['embed', 'iframe', 'object', 'script', 'style'],
    USE_PROFILES: { html: true },
  })
})
</script>

<template>
  <div class="message-content markdown-content" v-html="renderedContent" />
</template>
