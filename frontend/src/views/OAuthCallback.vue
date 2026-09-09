<template>
  <main style="max-width: 480px; margin: 15vh auto; padding: 24px; text-align: center">
    <h2>{{ error ? '认证未完成' : '正在完成认证…' }}</h2>
    <p v-if="error" role="alert">{{ error }}</p>
    <n-button v-if="error" type="primary" @click="router.replace('/?login=1')">返回登录</n-button>
    <n-spin v-else />
  </main>
</template>
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NSpin } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
const route = useRoute(), router = useRouter(), auth = useAuthStore()
const error = ref('')
const messages: Record<string, string> = {
  state: '登录请求已过期或失效，请在同一浏览器重新发起登录。',
  denied: '你已取消授权，可返回重新登录。',
  provider: '认证中心验证失败，请重试或联系管理员检查配置。',
  registration: '尚未绑定轻舟账号。请先注册并在账户设置中绑定；新账号还需符合本站邮箱验证要求。',
  email_exists: '此邮箱已有轻舟账号。请先用原账号登录，在账户设置中绑定认证中心。',
  account: '轻舟账号当前不可用，请联系管理员。',
  session: '原登录会话已失效，请重新登录后绑定。',
  binding: '绑定失败：该账号可能已有绑定，请联系管理员核实。',
  server: '暂时无法完成登录，请稍后重试。',
}
onMounted(async () => {
  if (route.query.error) { error.value = messages[String(route.query.error)] || messages.provider!; return }
  try {
    await auth.loginFromCookie()
    await router.replace(route.query.bound === '1' ? '/account' : '/dashboard')
  } catch (e: any) { error.value = e.message || '登录失败，请重试。' }
})
</script>
