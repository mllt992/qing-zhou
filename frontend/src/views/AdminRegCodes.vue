<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">注册码管理</h2>
        <p class="page-sub">批量生成、使用次数、用户组自动归属与完整核销记录</p>
      </div>
    </div>

    <div class="resource-overview">
      <div class="resource-metric"><b>{{ codes.length }}</b><span>全部注册码</span></div>
      <div class="resource-metric success"><b>{{ codes.filter(c => c.enabled && (!c.max_uses || (c.used || 0) < c.max_uses)).length }}</b><span>当前可用</span></div>
      <div class="resource-metric"><b>{{ codes.reduce((sum, c) => sum + (c.used || 0), 0) }}</b><span>累计核销次数</span></div>
      <div class="resource-metric"><b>{{ codes.filter(c => c.group_ids?.length).length }}</b><span>关联用户组</span></div>
    </div>

    <!-- 生成区 -->
    <n-card title="批量生成注册码" size="small" style="margin-bottom:16px;">
      <div class="rc-form">
        <div class="rc-field">
          <label>生成数量</label>
          <n-input-number v-model:value="genCount" :min="1" :max="100" style="width:100%;" />
        </div>
        <div class="rc-field">
          <label>单码最大使用次数（0 = 不限）</label>
          <n-input-number v-model:value="genMaxUses" :min="0" style="width:100%;" />
        </div>
        <div class="rc-field">
          <label>注册后自动加入用户组（可选）</label>
          <n-select v-model:value="genGroupIDs" :options="userGroupOptions" multiple clearable placeholder="用此码注册即加入" />
        </div>
        <div class="rc-field">
          <label>批次备注（可选）</label>
          <n-input v-model:value="genNote" placeholder="如：亲友邀请 / 社群活动" />
        </div>
        <div class="rc-submit">
          <n-button type="primary" :loading="generating" @click="handleGenerate">立即生成</n-button>
        </div>
      </div>

      <div v-if="genGroupIDs.length" style="margin-top:8px;font-size:12px;color:var(--text-3);">
        用这批注册码注册的用户将自动加入「<b>{{ groupNames(genGroupIDs) }}</b>」，从而可以购买对应用户组的专属套餐。
      </div>
      <div v-if="generatedCodes.length" class="gen-box">
        <div class="gen-head">
          <span style="font-size:12.5px;font-weight:650;color:var(--text);">✅ 已生成 {{ generatedCodes.length }} 个注册码</span>
          <n-button size="tiny" type="primary" @click="copyAll">复制全部</n-button>
        </div>
        <div class="gen-codes">{{ generatedCodes.join('\n') }}</div>
      </div>
    </n-card>

    <div class="page-toolbar">
      <n-radio-group v-model:value="statusFilter" size="small">
        <n-radio-button value="all">全部 ({{ codes.length }})</n-radio-button>
        <n-radio-button value="available">可用</n-radio-button>
        <n-radio-button value="exhausted">已用尽</n-radio-button>
        <n-radio-button value="disabled">已禁用</n-radio-button>
      </n-radio-group>
      <span class="spacer" />
      <n-input v-model:value="search" placeholder="搜索注册码、备注或核销用户" size="small" clearable style="width:230px;" />
    </div>

    <!-- 卡片列表 -->
    <n-spin :show="loading">
      <div v-if="filteredCodes.length" class="card-grid">
        <div v-for="r in pagedCodes" :key="r.id" class="list-card">
          <div class="lc-head">
            <code class="rc-code" @click="copyCode(r.code)" title="点击复制">{{ r.code }}</code>
            <n-tag :type="regStatusType(r)" size="tiny" :bordered="false">{{ regStatusLabel(r) }}</n-tag>
          </div>
          <div class="lc-meta">
            <span class="kv">核销进度 <b>{{ r.used || 0 }} / {{ r.max_uses || '∞' }}</b></span>
            <span class="kv">创建于 {{ fmtDateTime(r.created_at) }}</span>
          </div>
          <div v-if="r.note" class="lc-meta" style="color:var(--text-2);">{{ r.note }}</div>
          <div v-if="r.group_ids?.length" class="lc-meta">
            <span class="kv">归属用户组 <b>{{ groupNames(r.group_ids) }}</b></span>
          </div>
          <div v-if="r.uses && r.uses.length" class="rc-uses">
            <div class="rc-uses-head">核销记录（{{ r.uses.length }}）</div>
            <div v-for="(u, i) in r.uses.slice(0, 5)" :key="i" class="rc-use-row">
              <b>{{ u.username || u.email || '#' + u.user_id }}</b>
              <span>{{ fmtDateTime(u.used_at) }}</span>
            </div>
            <div v-if="r.uses.length > 5" class="rc-use-more">等共 {{ r.uses.length }} 人使用</div>
          </div>
          <div class="lc-foot">
            <n-button size="tiny" type="primary" secondary @click="copyCode(r.code)">复制</n-button>
            <n-button size="tiny" @click="toggleEnabled(r)">{{ r.enabled ? '禁用' : '启用' }}</n-button>
            <span style="flex:1;" />
            <n-button size="tiny" type="error" quaternary @click="handleDelete(r.id)">删除</n-button>
          </div>
        </div>
      </div>

      <div v-if="filteredCodes.length > pageSize" class="qz-pager">
        <span class="qz-pager-info">
          显示第 <b>{{ (page - 1) * pageSize + 1 }}–{{ Math.min(filteredCodes.length, page * pageSize) }}</b> 个，共 <b>{{ filteredCodes.length }}</b> 个注册码
        </span>
        <n-pagination v-model:page="page" v-model:page-size="pageSize" :item-count="filteredCodes.length" :page-sizes="[12, 24, 48, 96]" show-size-picker size="small" />
      </div>

      <n-empty v-else-if="!loading && !filteredCodes.length" :description="codes.length ? '没有匹配的注册码' : '暂无注册码'" style="padding:40px 0;" />
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { NCard, NInputNumber, NInput, NButton, NTag, NSpin, NSelect, NEmpty, NRadioGroup, NRadioButton, NPagination, useMessage } from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

const message = useMessage()
const codes = ref<any[]>([])
const loading = ref(false)
const generating = ref(false)
const genCount = ref(10)
const genMaxUses = ref(1)
const genNote = ref('')
const genGroupIDs = ref<number[]>([])
const generatedCodes = ref<string[]>([])
const statusFilter = ref<'all' | 'available' | 'exhausted' | 'disabled'>('all')
const search = ref('')
const page = ref(1)
const pageSize = ref(24)

watch([statusFilter, search, pageSize], () => { page.value = 1 })

const userGroups = ref<any[]>([])
const userGroupOptions = computed(() => userGroups.value.map(g => ({ label: g.name, value: g.id })))
function groupNames(ids?: number[]) {
  return (ids || []).map(id => userGroups.value.find(g => g.id === id)?.name).filter(Boolean).join('、')
}

function regStatusType(r: any): any {
  if (!r.enabled) return 'default'
  if (r.max_uses > 0 && (r.used || 0) >= r.max_uses) return 'warning'
  return 'success'
}
function regStatusLabel(r: any): string {
  if (!r.enabled) return '已禁用'
  if (r.max_uses > 0 && (r.used || 0) >= r.max_uses) return '已用尽'
  return '可用'
}

const filteredCodes = computed(() => {
  let list = codes.value
  if (statusFilter.value === 'available') {
    list = list.filter(r => r.enabled && (!r.max_uses || (r.used || 0) < r.max_uses))
  } else if (statusFilter.value === 'exhausted') {
    list = list.filter(r => r.enabled && r.max_uses > 0 && (r.used || 0) >= r.max_uses)
  } else if (statusFilter.value === 'disabled') {
    list = list.filter(r => !r.enabled)
  }
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter(r =>
      (r.code || '').toLowerCase().includes(q) ||
      (r.note || '').toLowerCase().includes(q) ||
      (r.uses || []).some((u: any) => (u.username || u.email || '').toLowerCase().includes(q))
    )
  }
  return list
})

const pagedCodes = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filteredCodes.value.slice(start, start + pageSize.value)
})

async function handleGenerate() {
  generating.value = true
  try {
    const data = await apiPost<any>('/api/admin/reg-codes/generate', {
      count: genCount.value, max_uses: genMaxUses.value, note: genNote.value, group_ids: genGroupIDs.value,
    })
    const newCodes = data?.codes || []
    generatedCodes.value = newCodes
    message.success(`已生成 ${newCodes.length} 个注册码`)
    await load()
  } catch (e: any) { message.error(e.message) } finally { generating.value = false }
}

async function copyCode(code: string) {
  if (await copyText(code)) message.success('已复制'); else message.error('复制失败，请手动复制')
}
async function copyAll() {
  if (await copyText(generatedCodes.value.join('\n'))) message.success('已复制全部'); else message.error('复制失败，请手动复制')
}

async function toggleEnabled(r: any) {
  try { await apiPut(`/api/admin/reg-codes/${r.id}`, { enabled: !r.enabled }); r.enabled = !r.enabled; message.success(r.enabled ? '已启用' : '已禁用') } catch (e: any) { message.error(e.message) }
}
async function handleDelete(id: number) {
  try { await apiDelete(`/api/admin/reg-codes/${id}`); message.success('已删除'); await load() } catch (e: any) { message.error(e.message) }
}

async function load() {
  loading.value = true
  try {
    const [cs, gs] = await Promise.all([
      apiList('/api/admin/reg-codes'),
      apiList('/api/admin/user-groups').catch(() => []),
    ])
    codes.value = cs; userGroups.value = gs
  } catch {} finally { loading.value = false }
}
onMounted(load)
</script>

<style scoped>
.rc-form {
  display: grid;
  grid-template-columns: 120px 190px minmax(180px, 1fr) minmax(160px, 1fr) auto;
  gap: 12px;
  align-items: end;
}
@media (max-width: 920px) {
  .rc-form { grid-template-columns: 1fr 1fr; }
}
.rc-field { display: flex; flex-direction: column; gap: 5px; }
.rc-field label { font-size: 12px; font-weight: 600; color: var(--text-2); }
.gen-box {
  margin-top: 14px; padding: 12px 14px;
  background: var(--bg-soft); border: 1px solid var(--border);
  border-radius: 10px;
}
.gen-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
.gen-codes {
  font-family: var(--mono); font-size: 12px; white-space: pre-wrap;
  word-break: break-all; color: var(--text-2); max-height: 160px; overflow-y: auto;
}
.rc-code {
  font-family: var(--mono); font-size: 13px; font-weight: 700;
  color: var(--accent-strong); background: var(--accent-soft);
  padding: 2px 8px; border-radius: 6px; cursor: pointer;
}
.rc-uses {
  padding: 8px 10px; background: var(--bg-soft);
  border: 1px solid rgba(15, 23, 42, .05); border-radius: 8px;
  font-size: 11.5px; display: flex; flex-direction: column; gap: 3px;
}
.rc-uses-head { font-size: 11px; color: var(--text-3); font-weight: 600; margin-bottom: 2px; }
.rc-use-row { display: flex; justify-content: space-between; gap: 8px; color: var(--text-2); }
.rc-use-row span { color: var(--text-3); font-variant-numeric: tabular-nums; }
.rc-use-more { font-size: 11px; color: var(--text-3); margin-top: 2px; }
</style>
