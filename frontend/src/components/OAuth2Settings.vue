<template>
  <n-card size="small" class="settings-section">
    <n-alert type="info" style="margin-bottom: 20px">在认证中心创建 OIDC 应用，使用授权码模式和 PKCE S256，允许 openid、profile、email，并登记下方完整回调地址。</n-alert>
    <n-alert v-if="loadError" type="error" style="margin-bottom: 16px">{{ loadError }} <n-button size="small" @click="load">重试</n-button></n-alert>
    <n-form label-placement="top" :disabled="!loaded || busy" style="max-width: 720px">
      <n-form-item label="启用认证中心登录"><n-switch v-model:value="form.enabled" /></n-form-item>
      <n-form-item label="登录按钮名称"><n-input v-model:value="form.name" placeholder="认证中心" :maxlength="80" /></n-form-item>
      <n-form-item label="Issuer 地址"><n-input v-model:value="form.issuer" placeholder="https://auth.example.com" /></n-form-item>
      <n-form-item label="Client ID"><n-input v-model:value="form.client_id" /></n-form-item>
      <n-form-item label="Client Secret"><n-input v-model:value="form.client_secret" type="password" show-password-on="click" placeholder="公共客户端可留空；*** 表示保留已保存密钥" :input-props="{ autocomplete: 'new-password' }" /></n-form-item>
      <n-form-item label="轻舟回调地址"><n-input v-model:value="form.redirect_url" placeholder="https://panel.example.com/api/auth/oauth2/callback" /></n-form-item>
      <n-form-item label="允许首次登录创建轻舟账号">
        <div><n-switch v-model:value="form.auto_register" /><p style="color: var(--text-3); font-size: 12px">仅在本站开放注册时生效，并遵守邮箱验证设置。已有账号需在账户设置中显式绑定，不会按邮箱自动合并。</p></div>
      </n-form-item>
      <n-space>
        <n-button :disabled="!loaded" :loading="busy" @click="test">测试认证中心连接</n-button>
        <n-button type="primary" :disabled="!loaded" :loading="busy" @click="save">保存认证配置</n-button>
      </n-space>
    </n-form>
  </n-card>
</template>
<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import { NCard, NAlert, NForm, NFormItem, NInput, NSwitch, NSpace, NButton, useMessage } from 'naive-ui'
import { apiGet, apiPut, apiPost } from '@/api'
import { useConfigStore } from '@/stores/config'
const message = useMessage(), config = useConfigStore()
const loaded = ref(false), busy = ref(false), loadError = ref('')
const form = reactive({ enabled: false, name: '认证中心', issuer: '', client_id: '', client_secret: '', redirect_url: '', auto_register: false })
async function load() {
  loaded.value = false
  try {
    Object.assign(form, await apiGet('/api/admin/oauth2'))
    if (!form.redirect_url) form.redirect_url = window.location.origin + '/api/auth/oauth2/callback'
    loaded.value = true; loadError.value = ''
  } catch (e: any) { loadError.value = e.message }
}
async function save() {
  busy.value = true
  try { Object.assign(form, await apiPut('/api/admin/oauth2', form)); await config.fetchConfig(); message.success('认证配置已保存') }
  catch (e: any) { message.error(e.message) } finally { busy.value = false }
}
async function test() {
  busy.value = true
  try { const result = await apiPost<{ message: string }>('/api/admin/oauth2/test', form); message.success(result.message, { duration: 6000 }) }
  catch (e: any) { message.error(e.message) } finally { busy.value = false }
}
onMounted(load)
</script>
