<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { forgotPassword, login, register, resetPassword, verifyEmail } from '@/lib/api'
import type { AuthSession, User } from '@/types'

type AuthMode = 'login' | 'register' | 'forgot' | 'reset' | 'verify' | 'message'

const props = defineProps<{
  initialAction?: 'verify' | 'reset'
  actionToken?: string
  hasSession?: boolean
}>()
const emit = defineEmits<{
  authenticated: [session: AuthSession]
  actionComplete: [action: 'verify' | 'reset', user?: User]
}>()

const mode = ref<AuthMode>(props.initialAction ?? 'login')
const displayName = ref('')
const email = ref('')
const password = ref('')
const passwordConfirm = ref('')
const passwordVisible = ref(false)
const loading = ref(false)
const error = ref('')
const message = ref('')

onMounted(() => {
  if (mode.value === 'verify') void verifyAccountEmail()
})

async function submit() {
  error.value = ''
  message.value = ''
  if (mode.value === 'reset' && password.value !== passwordConfirm.value) {
    error.value = '两次输入的新密码不一致'
    return
  }
  loading.value = true
  try {
    if (mode.value === 'login') {
      emit('authenticated', await login(email.value, password.value))
      return
    }
    if (mode.value === 'register') {
      emit('authenticated', await register(email.value, password.value, displayName.value))
      return
    }
    if (mode.value === 'forgot') {
      await forgotPassword(email.value)
      message.value = '如果该邮箱已注册，重置链接已经发送。请检查收件箱和垃圾邮件。'
      mode.value = 'message'
      return
    }
    if (mode.value === 'reset') {
      await resetPassword(props.actionToken ?? '', password.value)
      emit('actionComplete', 'reset')
      message.value = '密码已经重置，所有旧设备均已退出。请使用新密码重新登录。'
      mode.value = 'message'
    }
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '操作失败，请稍后再试'
  } finally {
    loading.value = false
  }
}

async function verifyAccountEmail() {
  loading.value = true
  error.value = ''
  try {
    const user = await verifyEmail(props.actionToken ?? '')
    emit('actionComplete', 'verify', user)
    if (!props.hasSession) {
      message.value = '邮箱验证成功，现在可以登录你的工作空间。'
      mode.value = 'message'
    }
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '验证失败，请稍后再试'
  } finally {
    loading.value = false
  }
}

function switchMode(next: AuthMode) {
  mode.value = next
  error.value = ''
  message.value = ''
  password.value = ''
  passwordConfirm.value = ''
}

function title() {
  if (mode.value === 'register') return '创建你的工作账户'
  if (mode.value === 'forgot') return '找回你的密码'
  if (mode.value === 'reset') return '设置新的密码'
  if (mode.value === 'verify') return '正在验证邮箱'
  if (mode.value === 'message') return '操作已完成'
  return '回到你的工作空间'
}

function description() {
  if (mode.value === 'register') return '仅需一分钟，开始你的第一段智能协作。'
  if (mode.value === 'forgot') return '输入注册邮箱，我们会发送一次性重置链接。'
  if (mode.value === 'reset') return '新密码设置后，所有已登录设备都会退出。'
  if (mode.value === 'verify') return '验证链接只能使用一次，请稍候。'
  if (mode.value === 'message') return message.value
  return '登录后继续你的对话与任务。'
}
</script>

<template>
  <main class="auth-page">
    <section class="auth-intro">
      <div class="brand-mark" aria-hidden="true"><i /><i /><i /><i /></div>
      <p class="eyebrow">AGENT LAB / INTELLIGENT WORKSPACE</p>
      <h1>让每一次对话，<br />推动真实工作。</h1>
      <p>面向个人与团队的多模型工作空间。用对话梳理想法、完成任务、沉淀上下文，让智能能力自然融入每一天。</p>
      <div class="intro-points">
        <span>多模型协作</span><span>实时响应</span><span>对话沉淀</span>
      </div>
    </section>

    <section class="auth-card-wrap">
      <form class="auth-card" @submit.prevent="submit">
        <div class="mobile-wordmark"><span class="wordmark-mark" aria-hidden="true"><i /><i /><i /><i /></span><span>Agent Workspace</span></div>
        <div>
          <p class="eyebrow">YOUR WORKSPACE</p>
          <h2>{{ title() }}</h2>
          <p class="muted" :role="mode === 'message' ? 'status' : undefined">{{ description() }}</p>
        </div>

        <template v-if="mode === 'login' || mode === 'register' || mode === 'forgot'">
          <label v-if="mode === 'register'">
            <span>昵称</span>
            <input v-model.trim="displayName" autocomplete="name" placeholder="例如：Edy" maxlength="80" />
          </label>
          <label>
            <span>邮箱</span>
            <input v-model.trim="email" type="email" inputmode="email" autocomplete="email" placeholder="you@example.com" required />
          </label>
        </template>

        <label v-if="mode === 'login' || mode === 'register' || mode === 'reset'">
          <span>{{ mode === 'reset' ? '新密码' : '密码' }}</span>
          <span class="password-field">
            <input
              v-model="password"
              :type="passwordVisible ? 'text' : 'password'"
              :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
              placeholder="8–72 个字节"
              minlength="8"
              maxlength="72"
              required
            />
            <button type="button" :aria-label="passwordVisible ? '隐藏密码' : '显示密码'" @click="passwordVisible = !passwordVisible">
              {{ passwordVisible ? '隐藏' : '显示' }}
            </button>
          </span>
        </label>
        <label v-if="mode === 'reset'">
          <span>确认新密码</span>
          <input v-model="passwordConfirm" :type="passwordVisible ? 'text' : 'password'" autocomplete="new-password" minlength="8" maxlength="72" required />
        </label>

        <p v-if="error" class="form-error" role="alert">{{ error }}</p>

        <button v-if="mode !== 'verify' && mode !== 'message'" class="primary-button" :disabled="loading">
          {{ loading ? '请稍候…' : mode === 'login' ? '登录' : mode === 'register' ? '注册并开始' : mode === 'forgot' ? '发送重置邮件' : '设置新密码' }}
        </button>
        <div v-else-if="mode === 'verify'" class="auth-progress" role="status"><span class="loading-mark" />{{ loading ? '正在验证邮箱…' : '验证未完成' }}</div>

        <p v-if="mode === 'login'" class="switch-mode">
          <button type="button" class="text-button" @click="switchMode('forgot')">忘记密码？</button>
          <span>还没有账户？</span>
          <button type="button" class="text-button" @click="switchMode('register')">创建账户</button>
        </p>
        <p v-else-if="mode === 'register' || mode === 'forgot' || mode === 'message'" class="switch-mode">
          <button type="button" class="text-button" @click="switchMode('login')">返回登录</button>
        </p>
      </form>
    </section>
  </main>
</template>
