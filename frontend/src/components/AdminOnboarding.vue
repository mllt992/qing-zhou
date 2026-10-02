<template>
  <n-card title="部署检查清单" size="small" style="margin-bottom:16px;">
    <template #header-extra>
      <n-button size="tiny" :loading="loading" @click="refresh">刷新</n-button>
    </template>
    <p class="onboarding-hint">可选引导，不会阻断操作。状态来自现有配置和最近检测；服务器页可主动重新检测或安装内核。</p>
    <n-alert v-if="error" type="warning">{{ error }}</n-alert>
    <p v-if="state?.complete" role="status">关键步骤已完成。可随时在此检查最新部署状态。</p>
    <ol v-if="state" class="onboarding-list">
      <li v-for="step in state.steps" :key="step.id">
        <div><n-tag :type="step.severity" size="small">{{ step.done ? '已完成' : step.optional ? '可选' : '待完成' }}</n-tag> <strong>{{ step.title }}</strong></div>
        <p :class="{ 'onboarding-error': step.severity === 'error' }">{{ step.detail }}</p>
        <n-button size="small" @click="router.push(step.path)">{{ step.id === 'metering' && !step.done ? '去检测 / 安装' : '去配置' }}</n-button>
      </li>
    </ol>
  </n-card>
</template>
<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NCard, NButton, NTag, NAlert } from 'naive-ui'
import { apiGet } from '@/api'
import { useAuthStore } from '@/stores/auth'
interface Step { id: string; title: string; detail: string; path: string; done: boolean; optional: boolean; severity: 'success'|'warning'|'error' }
interface Checklist { complete: boolean; steps: Step[]; checked_at: number }
const router = useRouter(), auth = useAuthStore()
const state = ref<Checklist|null>(null), error = ref(''), loading = ref(false)
const controller = new AbortController()
let active = true, timer: ReturnType<typeof setInterval>|undefined
async function refresh() {
  if (!auth.isAdmin || loading.value || !active) return
  loading.value = true
  try {
    const result = await apiGet<Checklist>('/api/admin/onboarding', { signal: controller.signal })
    if (active) { state.value = result; error.value = '' }
  } catch {
    if (active) error.value = '暂时无法读取部署状态，请稍后刷新；其他功能可以正常使用。'
  } finally { if (active) loading.value = false }
}
onMounted(() => { refresh(); timer = setInterval(() => { if (document.visibilityState !== 'hidden') refresh() }, 30000) })
onUnmounted(() => { active = false; controller.abort(); clearInterval(timer) })
</script>
<style scoped>
.onboarding-hint { color:var(--text-3);font-size:12px;line-height:1.6; }
.onboarding-list { padding-left:22px;display:grid;gap:16px; }
.onboarding-list p { color:var(--text-2);font-size:13px;margin:7px 0;line-height:1.6; }
.onboarding-list .onboarding-error { color:#b42318; }
</style>
