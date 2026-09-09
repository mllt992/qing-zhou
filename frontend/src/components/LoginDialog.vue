<template>
  <n-modal :show="show" @update:show="$emit('update:show', $event)" preset="card" class="login-modal" style="width: calc(100vw - 32px); max-width: 420px;" title="">
    <div class="login-brand">
      <div class="login-brand-inner">
        <div class="login-logo"><BrandMark :size="42" /></div>
        <div class="login-copy"><strong>轻舟</strong><span>安全连接，从这里开始</span></div>
      </div>
    </div>

    <n-tabs v-model:value="tab" animated class="auth-tabs">
      <n-tab-pane name="login" tab="登录">
        <n-form ref="loginFormRef" :model="loginForm" :rules="loginRules" label-placement="top">
          <n-form-item label="用户名" path="username">
            <n-input :input-props="{ autocomplete: 'username' }" v-model:value="loginForm.username" placeholder="请输入用户名" @keyup.enter="handleLogin" />
          </n-form-item>
          <n-form-item label="密码" path="password">
            <n-input :input-props="{ autocomplete: 'current-password' }" v-model:value="loginForm.password" type="password" show-password-on="click" placeholder="请输入密码" @keyup.enter="handleLogin" />
          </n-form-item>
          <n-button type="primary" block :loading="loading" @click="handleLogin">登录</n-button>
        </n-form>
      </n-tab-pane>

      <n-tab-pane v-if="config.config.registration_open" name="register" tab="注册">
        <n-form ref="regFormRef" :model="regForm" :rules="regRules" label-placement="top">
          <n-form-item label="用户名" path="username">
            <n-input v-model:value="regForm.username" placeholder="请输入用户名" />
          </n-form-item>
          <n-form-item label="密码" path="password">
            <n-input v-model:value="regForm.password" type="password" show-password-on="click" placeholder="请输入密码" />
          </n-form-item>
          <n-form-item label="邮箱" path="email">
            <n-input v-model:value="regForm.email" placeholder="请输入邮箱" />
          </n-form-item>
          <n-form-item v-if="config.config.register_mode === 'code'" label="邀请码" path="code">
            <n-input v-model:value="regForm.code" placeholder="请输入注册码" />
          </n-form-item>
          <n-button type="primary" block :loading="loading" @click="handleRegister">注册</n-button>
        </n-form>
      </n-tab-pane>

      <n-tab-pane name="forgot" tab="找回密码">
        <!-- 没配 SMTP 就发不出信，重置链接只会写进服务端日志。与其让人填完邮箱
             干等一封永远不来的邮件，不如直接说清楚该找谁。 -->
        <div v-if="!config.config.email_enabled" class="forgot-off">
          本站未配置邮件服务，无法自助重置密码。<br>
          请联系管理员帮你重置。
        </div>
        <n-form v-else label-placement="top">
          <n-form-item label="邮箱">
            <n-input v-model:value="forgotEmail" placeholder="请输入注册邮箱" @keyup.enter="handleForgot" />
          </n-form-item>
          <n-button type="primary" block :loading="loading" @click="handleForgot">发送重置邮件</n-button>
        </n-form>
      </n-tab-pane>
    </n-tabs>
    <n-button v-if="config.config.oauth2_enabled" block secondary :loading="oauthLoading" style="margin-top: 16px" @click="handleOAuth">使用{{ config.config.oauth2_name }}登录</n-button>
  </n-modal>
</template>

<script setup lang="ts">
import { ref, reactive, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NModal, NTabs, NTabPane, NForm, NFormItem, NInput, NButton, useMessage } from 'naive-ui'
import type { FormRules } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import BrandMark from './BrandMark.vue'
import { apiPost } from '@/api'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()

const router = useRouter()
const auth = useAuthStore()
const config = useConfigStore()
const message = useMessage()
const tab = ref('login')
const loading = ref(false)
const oauthLoading = ref(false)
async function handleOAuth() {
  oauthLoading.value = true
  try {
    const data = await apiPost<{ authorization_url: string }>('/api/auth/oauth2/start', {})
    window.location.assign(data.authorization_url)
  } catch (e: any) { message.error(e.message) } finally { oauthLoading.value = false }
}

const loginForm = reactive({ username: '', password: '' })
const regForm = reactive({ username: '', password: '', code: '', email: '' })
const forgotEmail = ref('')

const loginRules: FormRules = {
  username: { required: true, message: '请输入用户名', trigger: 'blur' },
  password: { required: true, message: '请输入密码', trigger: 'blur' },
}
const regRules: FormRules = {
  username: { required: true, message: '请输入用户名', trigger: 'blur' },
  password: { required: true, min: 6, message: '密码至少6位', trigger: 'blur' },
  email: {
    trigger: ['blur', 'input'],
    validator(_rule, value: string) {
      if (config.config.email_verify_required && config.config.register_mode !== 'code' && !value) {
        return new Error('需要邮箱以完成验证')
      }
      if (value && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) {
        return new Error('邮箱格式不正确')
      }
      return true
    },
  },
}

async function handleLogin() {
  loading.value = true
  try {
    await auth.login(loginForm.username, loginForm.password)
    message.success('登录成功')
    emit('update:show', false)
    router.push('/dashboard')
  } catch (e: any) {
    message.error(e.message || '登录失败')
  } finally {
    loading.value = false
  }
}

async function handleRegister() {
  loading.value = true
  try {
    const data = await auth.register(regForm.username, regForm.password, regForm.code || undefined, regForm.email || undefined)
    if (data?.need_verify) {
      message.success(data.message || '注册成功，请查收验证邮件后激活账号')
      tab.value = 'login'
      return
    }
    message.success('注册成功')
    emit('update:show', false)
    router.push('/dashboard')
  } catch (e: any) {
    message.error(e.message || '注册失败')
  } finally {
    loading.value = false
  }
}

async function handleForgot() {
  if (!forgotEmail.value) { message.warning('请输入邮箱'); return }
  loading.value = true
  try {
    // 用后端那句「若该邮箱已注册…」，别改写成「已发送，请查收」——后端刻意
    // 不确认这个邮箱存不存在，前端替它确认就把这层保护抵消了，而且对没注册过
    // 的邮箱来说那句话本身就是假的。
    const r = await apiPost<any>('/api/auth/forgot', { email: forgotEmail.value })
    message.success(r?.message || '若该邮箱已注册，我们已发送密码重置邮件')
  } catch (e: any) {
    message.error(e.message || '发送失败')
  } finally {
    loading.value = false
  }
}

watch(() => props.show, (v) => {
  if (v) { tab.value = 'login' }
})
</script>

<style scoped>
:global(.login-modal) {
  overflow: hidden;
  background: rgba(255,255,255,.93) !important;
  backdrop-filter: blur(24px) saturate(1.12);
  animation: loginIn .5s var(--ease-emphasized) both;
}
:global(.login-modal::before) {
  content: ''; position: absolute; inset: 0 0 auto; height: 96px; pointer-events: none;
  background: radial-gradient(circle at 18% 0, rgba(71,155,207,.11), transparent 70%);
}
@keyframes loginIn { from { opacity: 0; transform: translateY(10px) scale(.975); filter: blur(3px); } }
.login-brand { position: relative; text-align: center; margin-bottom: 18px; }
.login-brand-inner { display: inline-flex; align-items: center; gap: 11px; text-align: left; }
.login-logo {
  width: 42px; height: 42px; display: grid; place-items: center;
}
.login-copy { display: flex; flex-direction: column; line-height: 1.2; }
.login-copy strong { color: var(--text); font-size: 19px; font-weight: 700; letter-spacing: -.02em; }
.login-copy span { margin-top: 4px; color: var(--text-3); font-size: 10.5px; font-weight: 500; }
.auth-tabs :deep(.n-form-item-blank),
.auth-tabs :deep(.n-input) { width: 100%; min-width: 0; }
.auth-tabs :deep(.n-form-item-label) { padding-bottom: 6px; }
.auth-tabs :deep(.n-form-item-feedback-wrapper) { min-height: 14px !important; }
.forgot-off {
  padding: 18px 14px; text-align: center; line-height: 1.8;
  font-size: 13px; color: var(--text-2);
  background: var(--bg-soft); border-radius: 8px;
}
</style>
