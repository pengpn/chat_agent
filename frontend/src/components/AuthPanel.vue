<script setup lang="ts">
import { ref } from 'vue'
import { login, register } from '@/lib/api'
import type { AuthSession } from '@/types'

const emit = defineEmits<{ authenticated: [session: AuthSession] }>()
const mode = ref<'login' | 'register'>('login')
const displayName = ref('')
const email = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

async function submit() {
  error.value = ''
  loading.value = true
  try {
    const session = mode.value === 'login'
      ? await login(email.value, password.value)
      : await register(email.value, password.value, displayName.value)
    emit('authenticated', session)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '操作失败，请稍后再试'
  } finally {
    loading.value = false
  }
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
          <h2>{{ mode === 'login' ? '回到你的工作空间' : '创建你的工作账户' }}</h2>
          <p class="muted">{{ mode === 'login' ? '登录后继续你的对话与任务。' : '仅需一分钟，开始你的第一段智能协作。' }}</p>
        </div>

        <label v-if="mode === 'register'">
          <span>昵称</span>
          <input v-model.trim="displayName" autocomplete="name" placeholder="例如：Edy" maxlength="80" />
        </label>
        <label>
          <span>邮箱</span>
          <input v-model.trim="email" type="email" autocomplete="email" placeholder="you@example.com" required />
        </label>
        <label>
          <span>密码</span>
          <input v-model="password" type="password" autocomplete="current-password" placeholder="至少 8 个字符" minlength="8" required />
        </label>
        <p v-if="error" class="form-error">{{ error }}</p>
        <button class="primary-button" :disabled="loading">
          {{ loading ? '请稍候…' : mode === 'login' ? '登录' : '注册并开始' }}
        </button>
        <p class="switch-mode">
          {{ mode === 'login' ? '还没有账户？' : '已经有账户？' }}
          <button type="button" class="text-button" @click="mode = mode === 'login' ? 'register' : 'login'; error = ''">
            {{ mode === 'login' ? '创建账户' : '去登录' }}
          </button>
        </p>
      </form>
    </section>
  </main>
</template>
