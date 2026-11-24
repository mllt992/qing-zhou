<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">积分兑换码</h2>
        <p class="page-sub">一次性兑换、批量停用与到账审计。完整兑换码仅在生成时显示一次，请立即保存</p>
      </div>
    </div>

    <div class="resource-overview">
      <div class="resource-metric"><b>{{ codes.length }}</b><span>当前已加载兑换码</span></div>
      <div class="resource-metric success"><b>{{ codes.filter(c => c.status === 'unused').length }}</b><span>未使用（可兑换）</span></div>
      <div class="resource-metric"><b>{{ codes.filter(c => c.status === 'used').length }}</b><span>已核销兑换</span></div>
      <div class="resource-metric warn"><b>{{ codes.filter(c => c.status === 'expired' || c.status === 'disabled').length }}</b><span>已过期 / 已停用</span></div>
    </div>

    <n-card title="批量生成兑换码" size="small" style="margin-bottom:16px;">
      <form class="pc-form" @submit.prevent="generate">
        <div class="pc-field">
          <label>每张面额（积分）</label>
          <n-input-number v-model:value="points" :min="1" :max="1000000000" :precision="0" style="width:100%;" />
        </div>
        <div class="pc-field">
          <label>生成张数（1–100）</label>
          <n-input-number v-model:value="count" :min="1" :max="100" :precision="0" style="width:100%;" />
        </div>
        <div class="pc-field">
          <label>有效天数</label>
          <n-input-number v-model:value="days" :min="1" :max="3650" :precision="0" style="width:100%;" />
        </div>
        <div class="pc-field note-col">
          <label>批次 / 备注（可选）</label>
          <n-input v-model:value="note" :maxlength="256" placeholder="如：五一活动赠送 / 渠道 A" />
        </div>
        <div class="pc-submit">
          <n-button attr-type="submit" type="primary" :loading="generating" :disabled="generating">立即生成</n-button>
        </div>
      </form>

      <div v-if="generated.length" class="gen-box">
        <div class="gen-head">
          <span class="gen-warn">🔑 完整兑换码仅本次可见（共 {{ generated.length }} 张），离开页面后无法找回，数据库仅保存哈希</span>
          <n-button size="tiny" type="primary" @click="copyGenerated">复制全部兑换码</n-button>
        </div>
        <n-input :value="generated.join('\n')" type="textarea" readonly :autosize="{ minRows: 3, maxRows: 8 }" style="font-family:var(--mono);font-size:12px;" :input-props="{ 'aria-label': '新生成的兑换码' }" />
      </div>
    </n-card>

    <div class="page-toolbar">
      <n-select v-model:value="status" :options="statuses" size="small" style="width:150px;" @update:value="() => load(false)" aria-label="兑换码状态" />
      <n-input v-model:value="search" placeholder="搜索码尾、备注或使用人" size="small" clearable style="width:220px;" />
      <n-button size="small" :loading="loading" @click="load(false)">刷新</n-button>
      <span class="spacer" />
      <n-button v-if="selected.length" size="small" type="warning" secondary :disabled="disabling" :loading="disabling" @click="disableSelected">
        停用选中（{{ selected.length }}）
      </n-button>
    </div>

    <n-data-table
      :columns="columns"
      :data="filteredCodes"
      :row-key="row => row.id"
      v-model:checked-row-keys="selected"
      :loading="loading"
      :pagination="{ pageSize: 20, showSizePicker: true, pageSizes: [15, 20, 50, 100] }"
    />
    <div v-if="hasMore" style="margin-top:12px;text-align:center;">
      <n-button size="small" secondary :loading="loading" @click="load(true)">向后端加载更多历史批次</n-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, h, onMounted } from 'vue'
import { NCard, NInput, NInputNumber, NButton, NSelect, NDataTable, NTag, useMessage, useDialog } from 'naive-ui'
import type { DataTableColumns, DataTableRowKey } from 'naive-ui'
import { apiList, apiPost } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

interface PointCode { id: number; hint: string; points: number; note: string; status: string; expires_at: number; redeemed_username: string; redeemed_by: number; redeemed_at: number }
const message = useMessage(), dialog = useDialog()
const codes = ref<PointCode[]>([]), generated = ref<string[]>([]), selected = ref<DataTableRowKey[]>([])
const points = ref<number|null>(100), count = ref<number|null>(10), days = ref<number|null>(30), note = ref(''), status = ref('all'), search = ref('')
const loading = ref(false), generating = ref(false), disabling = ref(false), hasMore = ref(false)
const statuses = [
  { label: '全部状态', value: 'all' },
  { label: '未使用', value: 'unused' },
  { label: '已使用', value: 'used' },
  { label: '已过期', value: 'expired' },
  { label: '已停用', value: 'disabled' },
]

const filteredCodes = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return codes.value
  return codes.value.filter(c =>
    (c.hint || '').toLowerCase().includes(q) ||
    (c.note || '').toLowerCase().includes(q) ||
    (c.redeemed_username || '').toLowerCase().includes(q)
  )
})

function statusTagType(st: string): 'success' | 'info' | 'warning' | 'error' | 'default' {
  if (st === 'unused') return 'success'
  if (st === 'used') return 'info'
  if (st === 'expired') return 'warning'
  if (st === 'disabled') return 'error'
  return 'default'
}

const columns: DataTableColumns<PointCode> = [
  { type: 'selection', disabled: row => row.status !== 'unused' },
  { title: '码尾标识', key: 'hint', render: row => h('code', { style: 'font-family:var(--mono);font-weight:600;' }, row.hint) },
  { title: '面额 (积分)', key: 'points', render: row => h('b', { style: 'color:var(--text);font-variant-numeric:tabular-nums;' }, row.points) },
  { title: '批次 / 备注', key: 'note', render: row => row.note || '—' },
  {
    title: '状态',
    key: 'status',
    render: row => h(NTag, { type: statusTagType(row.status), size: 'tiny', bordered: false }, {
      default: () => statuses.find(s => s.value === row.status)?.label || row.status,
    }),
  },
  { title: '到期时间', key: 'expires_at', render: row => fmtDateTime(row.expires_at) },
  { title: '核销用户', key: 'redeemed_username', render: row => row.redeemed_username || (row.redeemed_by ? `#${row.redeemed_by}` : '—') },
  { title: '使用时间', key: 'redeemed_at', render: row => row.redeemed_at ? fmtDateTime(row.redeemed_at) : '—' },
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

<style scoped>
.pc-form {
  display: grid;
  grid-template-columns: repeat(3, minmax(120px, 160px)) minmax(180px, 1fr) auto;
  gap: 12px;
  align-items: end;
}
@media (max-width: 860px) {
  .pc-form { grid-template-columns: 1fr 1fr; }
  .pc-field.note-col { grid-column: 1 / -1; }
}
.pc-field { display: flex; flex-direction: column; gap: 5px; }
.pc-field label { font-size: 12px; font-weight: 600; color: var(--text-2); }
.gen-box {
  margin-top: 14px;
  padding: 12px 14px;
  background: var(--bg-soft);
  border: 1px solid rgba(217, 119, 6, .24);
  border-radius: 10px;
}
.gen-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.gen-warn { font-size: 12px; font-weight: 650; color: var(--warn); }
</style>
