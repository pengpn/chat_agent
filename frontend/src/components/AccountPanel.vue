<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import {
  changePassword,
  deleteAccount,
  downloadAccountExport,
  listAccountSessions,
  logoutAllSessions,
  resendEmailVerification,
  revokeAccountSession,
} from '@/lib/api'
import type { DeviceSession, User } from '@/types'

defineProps<{ user: User }>()
const emit = defineEmits<{ close: []; signedOut: []; deleted: [] }>()

const dialog = ref<HTMLElement | null>(null)
const sessions = ref<DeviceSession[]>([])
const sessionsLoading = ref(true)
const sessionError = ref('')
const emailStatus = ref('')
const emailLoading = ref(false)
const currentPassword = ref('')
const newPassword = ref('')
const newPasswordConfirm = ref('')
const passwordLoading = ref(false)
const passwordError = ref('')
const exportLoading = ref(false)
const exportStatus = ref('')
const showLogoutAllConfirm = ref(false)
const logoutAllLoading = ref(false)
const deletePassword = ref('')
const deleteConfirmation = ref('')
const deleteLoading = ref(false)
const deleteError = ref('')

onMounted(async () => {
  await nextTick()
  dialog.value?.focus()
  await loadSessions()
})

async function loadSessions() {
  sessionsLoading.value = true
  sessionError.value = ''
  try {
    sessions.value = await listAccountSessions()
  } catch (cause) {
    sessionError.value = messageFor(cause)
  } finally {
    sessionsLoading.value = false
  }
}

async function resendVerification() {
  emailLoading.value = true
  emailStatus.value = ''
  try {
    await resendEmailVerification()
    emailStatus.value = '验证邮件已发送，请检查收件箱和垃圾邮件。'
  } catch (cause) {
    emailStatus.value = messageFor(cause)
  } finally {
    emailLoading.value = false
  }
}

async function revokeSession(item: DeviceSession) {
  sessionError.value = ''
  try {
    const result = await revokeAccountSession(item.id)
    if (result.current) {
      emit('signedOut')
      return
    }
    sessions.value = sessions.value.filter((candidate) => candidate.id !== item.id)
  } catch (cause) {
    sessionError.value = messageFor(cause)
  }
}

async function logoutEverywhere() {
  logoutAllLoading.value = true
  sessionError.value = ''
  try {
    await logoutAllSessions()
    emit('signedOut')
  } catch (cause) {
    sessionError.value = messageFor(cause)
  } finally {
    logoutAllLoading.value = false
  }
}

async function submitPasswordChange() {
  passwordError.value = ''
  if (newPassword.value !== newPasswordConfirm.value) {
    passwordError.value = '两次输入的新密码不一致'
    return
  }
  passwordLoading.value = true
  try {
    await changePassword(currentPassword.value, newPassword.value)
    emit('signedOut')
  } catch (cause) {
    passwordError.value = messageFor(cause)
  } finally {
    passwordLoading.value = false
  }
}

async function exportData() {
  exportLoading.value = true
  exportStatus.value = ''
  try {
    await downloadAccountExport()
    exportStatus.value = '个人数据文件已经开始下载。'
  } catch (cause) {
    exportStatus.value = messageFor(cause)
  } finally {
    exportLoading.value = false
  }
}

async function submitAccountDeletion() {
  deleteError.value = ''
  deleteLoading.value = true
  try {
    await deleteAccount(deletePassword.value, deleteConfirmation.value)
    emit('deleted')
  } catch (cause) {
    deleteError.value = messageFor(cause)
  } finally {
    deleteLoading.value = false
  }
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function messageFor(cause: unknown) {
  return cause instanceof Error ? cause.message : '操作失败，请稍后再试'
}

function keepFocusInside(event: KeyboardEvent) {
  if (event.key !== 'Tab' || !dialog.value) return
  const items = Array.from(dialog.value.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex="0"]'))
  if (!items.length) return
  const first = items[0]
  const last = items[items.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
</script>

<template>
  <div class="account-modal-backdrop" @mousedown.self="emit('close')" @keydown.esc="emit('close')">
    <section ref="dialog" class="account-modal" role="dialog" aria-modal="true" aria-labelledby="account-title" tabindex="-1" @keydown="keepFocusInside">
      <header class="account-modal-header">
        <div><p class="eyebrow">ACCOUNT & SECURITY</p><h2 id="account-title">账户与安全</h2></div>
        <button class="account-close" type="button" aria-label="关闭账户设置" @click="emit('close')">
          <svg aria-hidden="true" viewBox="0 0 24 24"><path d="m6 6 12 12M18 6 6 18" /></svg>
        </button>
      </header>

      <div class="account-modal-content">
        <section class="account-section">
          <div class="account-section-heading"><div><h3>账户身份</h3><p>{{ user.email }}</p></div><span class="status-badge" :class="{ verified: user.emailVerifiedAt }">{{ user.emailVerifiedAt ? '邮箱已验证' : '邮箱待验证' }}</span></div>
          <button v-if="!user.emailVerifiedAt" class="secondary-button" type="button" :disabled="emailLoading" @click="resendVerification">{{ emailLoading ? '正在发送…' : '重新发送验证邮件' }}</button>
          <p v-if="emailStatus" class="account-inline-status" role="status">{{ emailStatus }}</p>
        </section>

        <section class="account-section">
          <div class="account-section-heading"><div><h3>登录设备</h3><p>撤销后，该设备现有的 Access Token 会立即失效。</p></div></div>
          <div v-if="sessionsLoading" class="account-loading" role="status"><span class="loading-mark" />正在加载设备…</div>
          <div v-else class="session-list">
            <div v-for="item in sessions" :key="item.id" class="session-row">
              <span class="session-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><rect x="4" y="5" width="16" height="12" rx="2" /><path d="M9 21h6m-3-4v4" /></svg></span>
              <div><strong>{{ item.deviceName }}</strong><span>{{ item.current ? '当前设备 · ' : '' }}最近使用 {{ formatDate(item.lastUsedAt) }}</span></div>
              <button class="text-action" type="button" @click="revokeSession(item)">{{ item.current ? '退出' : '撤销' }}</button>
            </div>
          </div>
          <p v-if="sessionError" class="account-error" role="alert">{{ sessionError }}</p>
          <div v-if="showLogoutAllConfirm" class="inline-confirm" role="group" aria-label="确认退出全部设备">
            <span>包括当前设备在内，全部退出？</span>
            <button type="button" :disabled="logoutAllLoading" @click="logoutEverywhere">确认退出</button>
            <button type="button" @click="showLogoutAllConfirm = false">取消</button>
          </div>
          <button v-else class="secondary-button" type="button" @click="showLogoutAllConfirm = true">退出全部设备</button>
        </section>

        <section class="account-section">
          <div class="account-section-heading"><div><h3>修改密码</h3><p>修改成功后会撤销包括当前设备在内的全部会话。</p></div></div>
          <form class="account-form" @submit.prevent="submitPasswordChange">
            <label><span>当前密码</span><input v-model="currentPassword" type="password" autocomplete="current-password" required /></label>
            <label><span>新密码</span><input v-model="newPassword" type="password" autocomplete="new-password" minlength="8" maxlength="72" required /></label>
            <label><span>确认新密码</span><input v-model="newPasswordConfirm" type="password" autocomplete="new-password" minlength="8" maxlength="72" required /></label>
            <p v-if="passwordError" class="account-error" role="alert">{{ passwordError }}</p>
            <button class="secondary-button" :disabled="passwordLoading">{{ passwordLoading ? '正在修改…' : '修改密码' }}</button>
          </form>
        </section>

        <section class="account-section">
          <div class="account-section-heading"><div><h3>个人数据</h3><p>导出账户资料、对话、消息、附件元数据和安全审计记录。</p></div></div>
          <button class="secondary-button" type="button" :disabled="exportLoading" @click="exportData">{{ exportLoading ? '正在准备…' : '导出 JSON 数据' }}</button>
          <p v-if="exportStatus" class="account-inline-status" role="status">{{ exportStatus }}</p>
        </section>

        <section class="account-section danger-zone">
          <div class="account-section-heading"><div><h3>注销账户</h3><p>永久删除账户、对话、消息和附件文件。此操作不可恢复。</p></div></div>
          <form class="account-form" @submit.prevent="submitAccountDeletion">
            <label><span>当前密码</span><input v-model="deletePassword" type="password" autocomplete="current-password" required /></label>
            <label><span>输入 DELETE 确认</span><input v-model="deleteConfirmation" autocomplete="off" pattern="DELETE" required /></label>
            <p v-if="deleteError" class="account-error" role="alert">{{ deleteError }}</p>
            <button class="danger-button" :disabled="deleteLoading || deleteConfirmation !== 'DELETE'">{{ deleteLoading ? '正在注销…' : '永久注销账户' }}</button>
          </form>
        </section>
      </div>
    </section>
  </div>
</template>
