<template>
  <n-modal :show="!!stepUpPrompt" :mask-closable="false" @update:show="onShowChange">
    <n-card style="width: min(440px, calc(100vw - 32px))" title="确认当前密码" role="dialog" aria-modal="true" aria-label="密码二次验证" closable @close="cancel">
      <p>即将{{ stepUpLabels[stepUpPrompt?.scope || ''] }}。请确认当前账户密码；同一操作范围 5 分钟内无需重复验证。</p>
      <form @submit.prevent="submit">
        <label for="step-up-password">当前密码</label>
        <n-input :input-props="{ id: 'step-up-password' }" v-model:value="password" type="password" autocomplete="current-password" placeholder="输入当前密码" :disabled="submitting" style="margin-top: 8px" />
        <p v-if="error" role="alert" style="color: var(--danger)">{{ error }}</p>
        <n-space justify="end" style="margin-top: 20px">
          <n-button @click="cancel">取消</n-button>
          <n-button type="primary" attr-type="submit" :loading="submitting" :disabled="!password || submitting">验证并继续</n-button>
        </n-space>
      </form>
    </n-card>
  </n-modal>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { NModal, NCard, NInput, NButton, NSpace } from 'naive-ui'
import { apiPost } from '@/api'
import { cancelStepUpPrompt, cancelStepUpRequests, finishStepUpPrompt, stepUpLabels, stepUpPrompt, type StepUpProof } from '@/api/reauth'

const password = ref('')
const submitting = ref(false)
const error = ref('')
let controller: AbortController | null = null
watch(() => stepUpPrompt.value?.id, () => {
  controller?.abort()
  controller = null
  password.value = ''
  error.value = ''
  submitting.value = false
}, { flush: 'sync' })
function cancel() {
  const id = stepUpPrompt.value?.id
  if (id !== undefined) cancelStepUpPrompt(id)
}
function onShowChange(show: boolean) { if (!show) cancel() }
async function submit() {
  const id = stepUpPrompt.value?.id
  const scope = stepUpPrompt.value?.scope
  if (id === undefined || !scope || !password.value || submitting.value) return
  controller = new AbortController()
  submitting.value = true
  error.value = ''
  const currentPassword = password.value
  password.value = ''
  try {
    const value = await apiPost<StepUpProof>('/api/user/reauth', { method: 'password', password: currentPassword, scope }, { signal: controller.signal })
    finishStepUpPrompt(id, value)
  } catch (err: any) {
    if (stepUpPrompt.value?.id === id && err?.name !== 'AbortError') error.value = err?.message || '身份验证失败'
  } finally {
    if (stepUpPrompt.value?.id === id) submitting.value = false
  }
}
onBeforeUnmount(() => { controller?.abort(); cancelStepUpRequests() })
</script>
