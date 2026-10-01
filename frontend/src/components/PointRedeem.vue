<template>
  <n-card title="兑换积分" size="small" style="margin: 12px 0 18px;">
    <form @submit.prevent="redeem" style="display:flex;gap:8px;flex-wrap:wrap;">
      <n-input v-model:value="code" :disabled="busy" :input-props="{ 'aria-label': '积分兑换码', autocomplete: 'off', maxlength: 128 }" placeholder="粘贴 QZ- 开头的积分兑换码" style="flex:1;min-width:240px;" />
      <n-button attr-type="submit" type="primary" :loading="busy" :disabled="busy || !code.trim()">兑换</n-button>
    </form>
    <p v-if="error" role="alert" style="color:var(--error-color,#b42318);">{{ error }}</p>
    <p v-if="success" role="status">{{ success }}</p>
    <p style="color:var(--text-3);font-size:12px;">兑换码一次有效，到账记录可在积分明细查看。没有兑换码或无法使用？请联系管理员。</p>
  </n-card>
</template>
<script setup lang="ts">
import { ref } from 'vue'
import { NCard, NInput, NButton } from 'naive-ui'
import { apiPost } from '@/api'
import { useAuthStore } from '@/stores/auth'
const emit = defineEmits<{ redeemed: [] }>()
const auth = useAuthStore()
const code = ref(''), busy = ref(false), error = ref(''), success = ref('')
async function redeem() {
  if (busy.value || !code.value.trim()) return
  busy.value = true; error.value = ''; success.value = ''
  try {
    const result = await apiPost<{ balance: number; points: number }>('/api/user/points/redeem', { code: code.value.trim() })
    if (auth.user) auth.user.points = result.balance
    code.value = ''; success.value = `已到账 ${result.points} 积分，当前余额 ${result.balance}`
    emit('redeemed')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '兑换失败，请稍后重试或联系管理员'
    // A lost response can follow a committed redemption. Do not auto-repeat the
    // write; refresh the balance and let the ledger establish what happened.
    await auth.fetchMe()
  } finally { busy.value = false }
}
</script>
