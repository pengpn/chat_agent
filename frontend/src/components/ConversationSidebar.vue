<script setup lang="ts">
import type { Conversation, User } from '@/types'

defineProps<{
  conversations: Conversation[]
  activeID: string | null
  user: User
}>()

const emit = defineEmits<{
  create: []
  select: [id: string]
  rename: [conversation: Conversation]
  remove: [conversation: Conversation]
  logout: []
}>()

function requestRename(conversation: Conversation) {
  const title = window.prompt('对话标题', conversation.title)
  if (title?.trim() && title.trim() !== conversation.title) emit('rename', { ...conversation, title: title.trim() })
}

function requestRemove(conversation: Conversation) {
  if (window.confirm(`确定删除“${conversation.title}”吗？此操作无法撤销。`)) emit('remove', conversation)
}
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-top">
      <div class="wordmark"><span class="wordmark-mark" aria-hidden="true"><i /><i /><i /><i /></span><span>Agent Workspace</span></div>
      <button class="new-chat" @click="emit('create')"><b>+</b> 新建对话</button>
    </div>
    <nav class="conversation-list" aria-label="对话列表">
      <p class="nav-label">工作对话</p>
      <div
        v-for="conversation in conversations"
        :key="conversation.id"
        class="conversation-row"
        :class="{ active: activeID === conversation.id }"
      >
        <button class="conversation-item" :title="conversation.title" @click="emit('select', conversation.id)">
          <span class="conversation-title">{{ conversation.title }}</span>
        </button>
        <span class="conversation-actions">
          <button :aria-label="`重命名 ${conversation.title}`" title="重命名" @click="requestRename(conversation)">···</button>
          <button :aria-label="`删除 ${conversation.title}`" title="删除" @click="requestRemove(conversation)">×</button>
        </span>
      </div>
      <p v-if="!conversations.length" class="empty-list">还没有对话<br />点击上方开始。</p>
    </nav>
    <div class="user-menu">
      <div class="avatar">{{ user.displayName.slice(0, 1).toUpperCase() }}</div>
      <div class="user-details"><strong>{{ user.displayName }}</strong><span>{{ user.email }}</span></div>
      <button title="退出登录" class="logout" @click="emit('logout')">退出</button>
    </div>
  </aside>
</template>
