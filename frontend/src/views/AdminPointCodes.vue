<template>
  <div>
    <h2 class="page-title">积分兑换码</h2>
    <p class="page-sub">一次性兑换、批量停用与到账审计。完整码只在生成后显示一次，请立即保存</p>
    <n-card title="生成兑换码" size="small" style="margin-bottom:16px;">
      <n-form label-placement="top" @submit.prevent="generate">
        <n-space align="end">
          <n-form-item label="每张积分"><n-input-number v-model:value="points" :min="1" :max="1000000000" :precision="0" /></n-form-item>
          <n-form-item label="张数"><n-input-number v-model:value="count" :min="1" :max="100" :precision="0" /></n-form-item>
          <n-form-item label="有效天数"><n-input-number v-model:value="days" :min="1" :max="3650" :precision="0" /></n-form-item>
          <n-form-item label="批次 / 备注"><n-input v-model:value="note" :maxlength="256" /></n-form-item>
          <n-form-item><n-button attr-type="submit" type="primary" :loading="generating" :disabled="generating">生成</n-button></n-form-item>
        </n-space>
      </n-form>
      <div v-if="generated.length">
        <n-alert type="warning" style="margin-bottom:8px;">完整兑换码仅本次可见。离开页面后无法找回，数据库只保存哈希。</n-alert>
        <n-input :value="generated.join('\n')" type="textarea" readonly :autosize="{ minRows: 3, maxRows: 10 }" :input-props="{ 'aria-label': '新生成的兑换码' }" />
        <n-button style="margin-top:8px;" @click="copyGenerated">复制全部</n-button>
      </div>
    </n-card>
    <n-space align="center" style="margin-bottom:12px;">
      <n-select v-model:value="status" :options="statuses" style="width:160px;" @update:value="() => load(false)" aria-label="兑换码状态" />
      <n-button :loading="loading" @click="load(false)">刷新</n-button>
      <n-button type="warning" :disabled="!selected.length || disabling" :loading="disabling" @click="disableSelected">停用选中（{{ selected.length }}）</n-button>
    </n-space>
    <n-data-table :columns="columns" :data="codes" :row-key="row => row.id" v-model:checked-row-keys="selected" :loading="loading" />
    <n-button v-if="hasMore" style="margin-top:12px;" :loading="loading" @click="load(true)">加载更多</n-button>
  </div>
</template>
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { NCard, NForm, NFormItem, NSpace, NInput, NInputNumber, NButton, NAlert, NSelect, NDataTable, useMessage, useDialog } from 'naive-ui'
import type { DataTableColumns, DataTableRowKey } from 'naive-ui'
import { apiList, apiPost } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'
interface PointCode { id: number; hint: string; points: number; note: string; status: string; expires_at: number; redeemed_username: string; redeemed_by: number; redeemed_at: number }
const message = useMessage(), dialog = useDialog()
const codes = ref<PointCode[]>([]), generated = ref<string[]>([]), selected = ref<DataTableRowKey[]>([])
const points = ref<number|null>(100), count = ref<number|null>(10), days = ref<number|null>(30), note = ref(''), status = ref('all')
const loading = ref(false), generating = ref(false), disabling = ref(false), hasMore = ref(false)
const statuses = [{ label: '全部', value: 'all' }, { label: '未使用', value: 'unused' }, { label: '已使用', value: 'used' }, { label: '已过期', value: 'expired' }, { label: '已停用', value: 'disabled' }]
const columns: DataTableColumns<PointCode> = [
  { type: 'selection', disabled: row => row.status !== 'unused' },
  { title: '码尾', key: 'hint' }, { title: '积分', key: 'points' }, { title: '批次 / 备注', key: 'note' },
  { title: '状态', key: 'status', render: row => statuses.find(s => s.value === row.status)?.label || row.status },
  { title: '到期时间', key: 'expires_at', render: row => fmtDateTime(row.expires_at) },
  { title: '使用人', key: 'redeemed_username', render: row => row.redeemed_username || (row.redeemed_by ? `#${row.redeemed_by}` : '—') },
  { title: '使用时间', key: 'redeemed_at', render: row => fmtDateTime(row.redeemed_at) },
]
let loadSequence = 0
async function load(more = false) {
  const seq = ++loadSequence; loading.value = true
  const before = more ? codes.value[codes.value.length - 1]?.id || 0 : 0
  try {
    const data = await apiList<PointCode>(`/api/admin/point-codes?status=${status.value}&before=${before}`)
    if (seq !== loadSequence) return
    codes.value = more ? [...codes.value, ...data] : data
    hasMore.value = data.length === 100
    if (!more) selected.value = []
  } catch (e: unknown) { if (seq === loadSequence) message.error(e instanceof Error ? e.message : '读取失败') }
  finally { if (seq === loadSequence) loading.value = false }
}
async function generate() {
  if (generating.value) return
  if (!points.value || !count.value || !days.value) { message.warning('请填写面额、张数和有效期'); return }
  generating.value = true
  try {
    const result = await apiPost<{ codes: { code: string }[] }>('/api/admin/point-codes/generate', { points: points.value, count: count.value, days: days.value, note: note.value })
    generated.value = result.codes.map(c => c.code); message.success(`已生成 ${generated.value.length} 张，请立即保存`)
    await load()
  } catch (e: unknown) { message.error(e instanceof Error ? e.message : '生成失败') }
  finally { generating.value = false }
}
async function copyGenerated() {
  if (await copyText(generated.value.join('\n'))) message.success('已复制')
  else message.error('复制失败，请手动选中复制')
}
function disableSelected() {
  const ids = selected.value.map(Number)
  dialog.warning({ title: '停用兑换码', content: `确认停用这 ${ids.length} 张尚未使用的兑换码？停用后不可兑换。`, positiveText: '停用', negativeText: '取消', onPositiveClick: async () => {
    if (disabling.value) return false
    disabling.value = true
    try { await apiPost('/api/admin/point-codes/disable', { ids }); message.success('已停用'); await load() }
    catch (e: unknown) { message.error(e instanceof Error ? e.message : '停用失败'); return false }
    finally { disabling.value = false }
  } })
}
onMounted(() => load())
</script>
