<script setup lang="ts">
import { nextTick, ref } from 'vue'
import type { Conversation, User } from '@/types'

defineProps<{
  conversations: Conversation[]
  activeID: string | null
  user: User
}>()

const emit = defineEmits<{
  create: []
  select: [id: string]
  rename: [conversation: Conversation, done: (success: boolean, message?: string) => void]
  remove: [conversation: Conversation]
  logout: []
}>()

const editingID = ref<string | null>(null)
const draftTitle = ref('')
const renameError = ref('')
const savingID = ref<string | null>(null)
const renameInput = ref<HTMLInputElement | null>(null)

async function startRename(conversation: Conversation) {
  if (savingID.value) return
  editingID.value = conversation.id
  draftTitle.value = conversation.title
  renameError.value = ''
  await nextTick()
  renameInput.value?.focus()
  renameInput.value?.select()
}

function cancelRename() {
  if (savingID.value) return
  editingID.value = null
  draftTitle.value = ''
  renameError.value = ''
}

function saveRename(conversation: Conversation) {
  const title = draftTitle.value.trim()
  if (!title) {
    renameError.value = '请输入对话名称'
    void nextTick(() => renameInput.value?.focus())
    return
  }
  if (title === conversation.title) {
    cancelRename()
    return
  }

  savingID.value = conversation.id
  renameError.value = ''
  emit('rename', { ...conversation, title }, (success, message) => {
    savingID.value = null
    if (success) {
      cancelRename()
      return
    }
    renameError.value = message || '重命名失败，请重试'
    void nextTick(() => renameInput.value?.focus())
  })
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
        :class="{ active: activeID === conversation.id, editing: editingID === conversation.id }"
      >
        <button v-if="editingID !== conversation.id" class="conversation-item" :title="conversation.title" @click="emit('select', conversation.id)">
          <span class="conversation-title">{{ conversation.title }}</span>
        </button>
        <form v-else class="conversation-rename-form" @submit.prevent="saveRename(conversation)">
          <label class="sr-only" :for="`conversation-title-${conversation.id}`">对话名称</label>
          <input
            :id="`conversation-title-${conversation.id}`"
            ref="renameInput"
            v-model="draftTitle"
            class="conversation-rename-input"
            :aria-describedby="renameError ? `conversation-rename-error-${conversation.id}` : undefined"
            :disabled="savingID === conversation.id"
            maxlength="120"
            @keydown.esc.prevent="cancelRename"
          />
          <span class="conversation-rename-actions">
            <button class="rename-icon-button save" type="submit" :disabled="savingID === conversation.id" :aria-label="`保存 ${conversation.title}`" title="保存">
              <svg v-if="savingID !== conversation.id" viewBox="0 0 16 16" aria-hidden="true"><path d="m3 8.5 3 3L13 4.5" /></svg>
              <span v-else class="rename-spinner" aria-label="正在保存" />
            </button>
            <button class="rename-icon-button cancel" type="button" :disabled="savingID === conversation.id" :aria-label="`取消重命名 ${conversation.title}`" title="取消" @click="cancelRename">
              <svg viewBox="0 0 16 16" aria-hidden="true"><path d="m4 4 8 8m0-8-8 8" /></svg>
            </button>
          </span>
          <p v-if="renameError" :id="`conversation-rename-error-${conversation.id}`" class="conversation-rename-error" role="alert">{{ renameError }}</p>
        </form>
        <span v-if="editingID !== conversation.id" class="conversation-actions">
          <button :aria-label="`重命名 ${conversation.title}`" title="重命名" @click="startRename(conversation)">···</button>
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
