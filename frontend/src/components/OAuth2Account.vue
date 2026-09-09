<template>
  <n-card v-if="state.enabled || error" :title="state.name || '认证中心'" size="small" style="margin-bottom: 16px">
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-tag v-if="state.bound" type="success">已绑定，可使用认证中心登录此账号</n-tag>
    <n-form v-else-if="state.enabled" label-placement="top" style="max-width: 420px" @submit.prevent="bind">
      <p>绑定后可通过认证中心登录当前轻舟账号。请输入当前轻舟密码，再前往认证中心授权。</p>
      <n-form-item label="当前轻舟密码"><n-input v-model:value="password" type="password" show-password-on="click" :input-props="{ autocomplete: 'current-password' }" /></n-form-item>
      <n-button type="primary" :disabled="!password" :loading="busy" @click="bind">前往认证中心绑定</n-button>
    </n-form>
  </n-card>
</template>
<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import { NCard, NAlert, NTag, NForm, NFormItem, NInput, NButton } from 'naive-ui'
import { apiGet, apiPost } from '@/api'
const state = reactive({ enabled: false, name: '', bound: false })
const password = ref(''), error = ref(''), busy = ref(false)
onMounted(async () => { try { Object.assign(state, await apiGet('/api/user/oauth2')) } catch (e: any) { error.value = e.message } })
async function bind() {
  busy.value = true; error.value = ''
  try { const data = await apiPost<{ authorization_url: string }>('/api/user/oauth2/bind', { password: password.value }); password.value = ''; window.location.assign(data.authorization_url) }
  catch (e: any) { error.value = e.message } finally { busy.value = false }
}
</script>
