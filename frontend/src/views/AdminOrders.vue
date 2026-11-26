<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">订单管理</h2>
        <p class="page-sub">收入、退款、套餐归属与每笔订单的完整状态轨迹</p>
      </div>
    </div>

    <div class="resource-overview">
      <div class="resource-metric success"><b>{{ stats.revenue }} 积分</b><span>净留存总收入</span></div>
      <div class="resource-metric warn"><b>{{ stats.refunded }} 积分</b><span>累计已退积分</span></div>
      <div class="resource-metric"><b>{{ orders.length }}</b><span>全部订单总数</span></div>
      <div class="resource-metric"><b>{{ stats.refundRate }}%</b><span>订单退款率</span></div>
    </div>

    <div class="orders-toolbar">
      <div class="seg">
        <button v-for="t in tabs" :key="t.key" class="seg-btn" :class="{ active: statusTab === t.key }" @click="statusTab = t.key">
          {{ t.label }}<span class="seg-count">{{ t.count }}</span>
        </button>
      </div>
      <div class="seg">
        <span class="seg-label">视图</span>
        <button v-for="g in groupOpts" :key="g.key" class="seg-btn" :class="{ active: groupBy === g.key }" @click="groupBy = g.key">
          {{ g.label }}
        </button>
      </div>
      <n-button v-if="groupBy !== 'none' && allGroupKeys.length > 1" size="small" quaternary @click="toggleAllGroups">
        {{ collapsedGroups.size === allGroupKeys.length ? '展开全部分组' : '折叠全部分组' }}
      </n-button>
      <span class="spacer" />
      <n-input v-model:value="search" placeholder="搜索用户名或套餐名称" size="small" style="width:230px;" clearable />
    </div>

    <div v-if="pageItems.length" class="batch-bar" :class="{ active: selected.size }">
      <n-checkbox :checked="allPageSelected" :indeterminate="somePageSelected && !allPageSelected" @update:checked="togglePage">本页全选</n-checkbox>
      <template v-if="selected.size">
        <span class="bb-count">已选 <b>{{ selected.size }}</b> 单</span>
        <a v-if="selected.size < view.length" class="bb-link" @click="selectAllResults">选择筛选结果全部 {{ view.length }} 单</a>
        <a class="bb-link" @click="clearSel">取消选择</a>
        <span class="spacer" />
        <n-button size="small" type="error" secondary :loading="batching" @click="batchDelete">批量删除</n-button>
      </template>
      <span v-else class="bb-hint">勾选订单后可批量删除</span>
    </div>

    <n-spin :show="loading">
      <!-- 桌面端：现代数据表格 -->
      <div v-if="view.length" class="orders-panel">
        <div class="orders-table-wrap">
          <table class="orders-table">
            <thead>
              <tr>
                <th class="chk-col"><n-checkbox :checked="allPageSelected" :indeterminate="somePageSelected && !allPageSelected" @update:checked="togglePage" /></th>
                <th style="width:72px;">单号</th>
                <th>套餐名称</th>
                <th>用户</th>
                <th>类型</th>
                <th class="sortable num" @click="toggleSort('price_points')">金额 (积分)<span class="sort-ind">{{ sortInd('price_points') }}</span></th>
                <th>状态</th>
                <th class="sortable" @click="toggleSort('created_at')">下单时间<span class="sort-ind">{{ sortInd('created_at') }}</span></th>
                <th class="act-col">操作</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="g in pagedGroups" :key="g.key">
                <tr v-if="g.label" class="group-row" @click="toggleGroupCollapse(g.key)">
                  <td :colspan="9">
                    <div class="group-bar">
                      <span class="group-chev" :class="{ closed: collapsedGroups.has(g.key) }">▾</span>
                      <span class="group-name">{{ g.label }}</span>
                      <span class="group-badge">{{ g.totalCount }} 单</span>
                      <span class="group-meta">净收入 <b>{{ g.revenue }}</b> 积分</span>
                    </div>
                  </td>
                </tr>
                <template v-if="!g.label || !collapsedGroups.has(g.key)">
                  <tr v-for="o in g.items" :key="o.id" :class="{ refunded: o.status === 'refunded', picked: selected.has(o.id) }">
                    <td class="chk-col"><n-checkbox :checked="selected.has(o.id)" @update:checked="(v: boolean) => toggleSel(o.id, v)" /></td>
                    <td class="id-col">#{{ o.id }}</td>
                    <td class="strong">{{ o.name || '—' }}</td>
                    <td>
                      <span class="user-cell" :class="{ muted: !o.username }">
                        <i v-if="o.username" class="user-dot">{{ o.username.charAt(0).toUpperCase() }}</i>
                        {{ o.username || '已删除' }}
                      </span>
                    </td>
                    <td>
                      <n-tag :type="o.type === 'plan' ? 'success' : 'info'" size="tiny" :bordered="false">
                        {{ typeText(o.type) }}
                      </n-tag>
                    </td>
                    <td class="num">
                      <span class="amount">{{ o.price_points }}</span>
                      <span v-if="o.status === 'refunded'" class="amount-sub">
                        退 {{ o.refunded_points }}<template v-if="o.refund_ratio > 0 && o.refund_ratio < 1"> ({{ Math.round(o.refund_ratio * 100) }}%)</template>
                      </span>
                    </td>
                    <td>
                      <span class="pill" :class="o.status === 'success' ? 'pill-ok' : 'pill-warn'">
                        <i class="pill-dot" />{{ o.status === 'success' ? '成功' : '已退款' }}
                      </span>
                    </td>
                    <td class="time">{{ fmtDateTime(o.created_at) }}</td>
                    <td class="act-col">
                      <n-button v-if="o.status === 'success'" size="tiny" type="warning" secondary @click="openRefund(o.id)">退款</n-button>
                      <n-button size="tiny" type="error" quaternary @click="handleDelete(o)">删除</n-button>
                    </td>
                  </tr>
                </template>
              </template>
            </tbody>
          </table>
        </div>

        <!-- 底部统一分页栏 -->
        <div class="qz-pager">
          <span class="qz-pager-info">
            显示第 <b>{{ pageStart }}–{{ pageEnd }}</b> 条，共 <b>{{ view.length }}</b> 条订单
          </span>
          <div class="qz-pager-controls">
            <n-pagination
              v-model:page="page"
              v-model:page-size="pageSize"
              :item-count="view.length"
              :page-sizes="[15, 30, 50, 100]"
              show-size-picker
              size="small"
            />
          </div>
        </div>
      </div>

      <!-- 移动端：卡片（同样接入分页） -->
      <div v-if="view.length" class="orders-cards">
        <div v-for="g in pagedGroups" :key="g.key" class="order-group">
          <div v-if="g.label" class="group-head" @click="toggleGroupCollapse(g.key)">
            <span class="group-chev" :class="{ closed: collapsedGroups.has(g.key) }">▾</span>
            <span class="group-name">{{ g.label }}</span>
            <span class="group-meta">{{ g.totalCount }} 单 · 收入 {{ g.revenue }} 积分</span>
          </div>
          <div v-if="!g.label || !collapsedGroups.has(g.key)" class="card-grid">
            <div v-for="o in g.items" :key="o.id" class="list-card">
              <div class="lc-head">
                <n-checkbox :checked="selected.has(o.id)" @update:checked="(v: boolean) => toggleSel(o.id, v)" />
                <span class="lc-title"><span class="id-col">#{{ o.id }}</span> {{ o.name || '—' }}</span>
                <span class="pill" :class="o.status === 'success' ? 'pill-ok' : 'pill-warn'">{{ o.status === 'success' ? '成功' : '已退款' }}</span>
              </div>
              <div class="lc-meta">
                <span class="kv">用户 <b>{{ o.username || '已删除' }}</b></span>
                <span class="kv"><n-tag :type="o.type === 'plan' ? 'success' : 'info'" size="tiny" :bordered="false">{{ typeText(o.type) }}</n-tag></span>
                <span class="kv">积分 <b>{{ o.price_points }}</b></span>
                <span class="kv" v-if="o.status === 'refunded'">已退 <b style="color:var(--warn);">{{ o.refunded_points }}</b>
                  <template v-if="o.refund_ratio > 0 && o.refund_ratio < 1">（{{ Math.round(o.refund_ratio * 100) }}%）</template>
                </span>
                <span class="kv">{{ fmtDateTime(o.created_at) }}</span>
              </div>
              <div class="lc-foot">
                <n-button v-if="o.status === 'success'" size="tiny" type="warning" secondary @click="openRefund(o.id)">退款</n-button>
                <n-button size="tiny" type="error" quaternary @click="handleDelete(o)">删除</n-button>
              </div>
            </div>
          </div>
        </div>
        <div class="qz-pager" style="margin-top:12px;">
          <span class="qz-pager-info">共 <b>{{ view.length }}</b> 单</span>
          <n-pagination v-model:page="page" v-model:page-size="pageSize" :item-count="view.length" :page-slot="5" size="small" />
        </div>
      </div>

      <n-empty v-if="!view.length && !loading" :description="orders.length ? '没有符合条件的订单' : '暂无订单'" style="padding:48px 0;">
        <template v-if="orders.length" #extra><n-button size="small" @click="statusTab='all'; groupBy='none'; search=''">清除筛选</n-button></template>
      </n-empty>
    </n-spin>

    <refund-dialog v-model:show="refundShow" :order-id="refundId" @done="load" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { NSpin, NInput, NButton, NTag, NEmpty, NPagination, NCheckbox, useMessage, useDialog } from 'naive-ui'
import { apiList, apiDelete } from '@/api'
import { fmtDateTime } from '@/utils/format'
import RefundDialog from '@/components/RefundDialog.vue'

const message = useMessage()
const dialog = useDialog()
const orders = ref<any[]>([])
const loading = ref(false)
const search = ref('')
const statusTab = ref<'all' | 'success' | 'refunded'>('all')
const groupBy = ref<'none' | 'plan' | 'status'>('none')
const sortKey = ref<'created_at' | 'price_points'>('created_at')
const sortDir = ref<'asc' | 'desc'>('desc')
const refundShow = ref(false)
const refundId = ref<number | null>(null)

// 分页与分组折叠状态
const page = ref(1)
const pageSize = ref(20)
const collapsedGroups = ref<Set<string>>(new Set())

watch([statusTab, groupBy, search, sortKey, sortDir, pageSize], () => {
  page.value = 1
  selected.value = new Set()
})

const tabs = computed(() => [
  { key: 'all' as const, label: '全部', count: orders.value.length },
  { key: 'success' as const, label: '成功', count: orders.value.filter((o: any) => o.status === 'success').length },
  { key: 'refunded' as const, label: '已退款', count: orders.value.filter((o: any) => o.status === 'refunded').length },
])

const groupOpts = [
  { key: 'none' as const, label: '列表分页' },
  { key: 'plan' as const, label: '按套餐分组' },
  { key: 'status' as const, label: '按状态分组' },
]

function typeText(t: string): string {
  if (t === 'plan') return '订阅计划'
  if (t === 'traffic') return '流量包'
  return t || '—'
}

// 保留收入口径：成功计全额，部分退款计留存部分
function retained(o: any): number {
  if (o.status === 'success') return o.price_points || 0
  if (o.status === 'refunded') return (o.price_points || 0) - (o.refunded_points || 0)
  return 0
}

// view = 按标签 + 搜索过滤后再排序
const view = computed(() => {
  let list = orders.value
  if (statusTab.value !== 'all') list = list.filter((o: any) => o.status === statusTab.value)
  if (search.value) {
    const q = search.value.trim().toLowerCase()
    list = list.filter((o: any) =>
      o.username?.toLowerCase().includes(q) ||
      o.name?.toLowerCase().includes(q) ||
      String(o.id).includes(q)
    )
  }
  const k = sortKey.value, dir = sortDir.value === 'asc' ? 1 : -1
  return [...list].sort((a: any, b: any) => ((a[k] || 0) - (b[k] || 0)) * dir)
})

// 全量分组统计（用于组头显示完整单量与收入）
const fullGroups = computed(() => {
  const rows = view.value
  if (groupBy.value === 'none') {
    return [{ key: '__all', label: '', totalCount: rows.length, revenue: 0, items: rows }]
  }
  const map = new Map<string, any[]>()
  for (const o of rows) {
    const key = groupBy.value === 'plan' ? (o.name || '—') : o.status
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(o)
  }
  const arr = [...map.entries()].map(([key, items]) => ({
    key,
    label: groupBy.value === 'status' ? (key === 'success' ? '成功订单' : '已退款订单') : key,
    totalCount: items.length,
    revenue: items.reduce((s, o) => s + retained(o), 0),
    items,
  }))
  if (groupBy.value === 'status') arr.sort((a, b) => (a.key === 'success' ? 0 : 1) - (b.key === 'success' ? 0 : 1))
  else arr.sort((a, b) => b.totalCount - a.totalCount || b.revenue - a.revenue)
  return arr
})

const allGroupKeys = computed(() => fullGroups.value.filter(g => g.label).map(g => g.key))

function toggleGroupCollapse(key: string) {
  const next = new Set(collapsedGroups.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsedGroups.value = next
}
function toggleAllGroups() {
  if (collapsedGroups.value.size === allGroupKeys.value.length) {
    collapsedGroups.value = new Set()
  } else {
    collapsedGroups.value = new Set(allGroupKeys.value)
  }
}

// 按分页窗口切片（即使在分组视图下也按页展示，避免几百单一次性渲染到底）
const pageStart = computed(() => view.value.length ? (page.value - 1) * pageSize.value + 1 : 0)
const pageEnd = computed(() => Math.min(view.value.length, page.value * pageSize.value))

const pagedGroups = computed(() => {
  const start = (page.value - 1) * pageSize.value
  const end = start + pageSize.value
  if (groupBy.value === 'none') {
    return [{
      key: '__all',
      label: '',
      totalCount: view.value.length,
      revenue: 0,
      items: view.value.slice(start, end),
    }]
  }
  const out: any[] = []
  let cursor = 0
  for (const g of fullGroups.value) {
    const gStart = cursor
    const gEnd = cursor + g.items.length
    cursor = gEnd
    if (gEnd <= start || gStart >= end) continue
    const sliceStart = Math.max(0, start - gStart)
    const sliceEnd = Math.min(g.items.length, end - gStart)
    out.push({
      ...g,
      items: g.items.slice(sliceStart, sliceEnd),
    })
  }
  return out
})

// ---- 多选 / 批量删除 ----
const selected = ref<Set<number>>(new Set())
const batching = ref(false)
const pageItems = computed(() => pagedGroups.value.flatMap((g: any) => g.items))
const allPageSelected = computed(() => pageItems.value.length > 0 && pageItems.value.every((o: any) => selected.value.has(o.id)))
const somePageSelected = computed(() => pageItems.value.some((o: any) => selected.value.has(o.id)))
function toggleSel(id: number, v: boolean) {
  const n = new Set(selected.value)
  if (v) n.add(id); else n.delete(id)
  selected.value = n
}
function togglePage(v: boolean) {
  const n = new Set(selected.value)
  for (const o of pageItems.value) { if (v) n.add(o.id); else n.delete(o.id) }
  selected.value = n
}
function selectAllResults() { selected.value = new Set(view.value.map((o: any) => o.id)) }
function clearSel() { selected.value = new Set() }
function batchDelete() {
  const ids = [...selected.value]
  if (!ids.length) return
  dialog.warning({
    title: '确认批量删除订单',
    content: `确定删除选中的 ${ids.length} 笔订单？订单记录将永久消失（不会退还积分），此操作不可撤销。`,
    positiveText: `删除 ${ids.length} 单`, negativeText: '取消',
    onPositiveClick: async () => {
      batching.value = true
      let ok = 0; const failed: string[] = []
      for (const id of ids) {
        try { await apiDelete(`/api/admin/orders/${id}`); ok++ }
        catch (e: any) { failed.push(`#${id} ${e?.message || '失败'}`) }
      }
      batching.value = false
      selected.value = new Set()
      if (failed.length) message.warning(`已删除 ${ok} 单，${failed.length} 单失败：${failed.slice(0, 3).join('；')}`, { duration: 8000 })
      else message.success(`已删除 ${ok} 单`)
      await load()
    },
  })
}

function toggleSort(k: 'created_at' | 'price_points') {
  if (sortKey.value === k) sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  else { sortKey.value = k; sortDir.value = 'desc' }
}
function sortInd(k: 'created_at' | 'price_points') {
  if (sortKey.value !== k) return ''
  return sortDir.value === 'asc' ? ' ↑' : ' ↓'
}

const stats = computed(() => {
  let revenue = 0, refunded = 0, refundedCount = 0
  for (const o of orders.value) {
    if (o.status === 'success') revenue += o.price_points || 0
    if (o.status === 'refunded') { refunded += o.refunded_points || 0; revenue += (o.price_points || 0) - (o.refunded_points || 0); refundedCount++ }
  }
  const refundRate = orders.value.length ? Math.round((refundedCount / orders.value.length) * 100) : 0
  return { revenue, refunded, refundRate }
})

function openRefund(id: number) {
  refundId.value = id
  refundShow.value = true
}
function handleDelete(o: any) {
  dialog.warning({
    title: '确认删除订单',
    content: '删除后该订单记录将永久消失（仅用于清理已删除用户的孤儿订单）。确定删除？',
    positiveText: '删除', negativeText: '取消',
    onPositiveClick: async () => {
      try { await apiDelete(`/api/admin/orders/${o.id}`); message.success('已删除'); await load() }
      catch (e: any) { message.error(e.message) }
    },
  })
}

async function load() {
  loading.value = true
  try { orders.value = await apiList('/api/admin/orders') }
  catch (e: any) { message.error('加载失败：' + (e?.message || '请稍后重试')) }
  finally { loading.value = false }
}
onMounted(load)
</script>

<style scoped>
.orders-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  margin-bottom: 14px;
}
.orders-toolbar .spacer { flex: 1; }

/* 批量操作条 */
.batch-bar {
  display: flex; align-items: center; gap: 14px; flex-wrap: wrap; min-height: 40px;
  padding: 8px 14px; margin-bottom: 12px; font-size: 12.5px;
  background: var(--bg-soft); border: 1px solid var(--border); border-radius: 10px;
}
.batch-bar.active { background: var(--accent-soft); border-color: rgba(15, 108, 189, .22); }
.bb-count b { color: var(--accent-strong); }
.bb-hint { color: var(--text-3); }
.bb-link { color: var(--accent-strong); cursor: pointer; }
.bb-link:hover { text-decoration: underline; }
.batch-bar .spacer { flex: 1; }
.list-card .lc-head { align-items: center; gap: 8px; }

/* 分段筛选 */
.seg {
  display: inline-flex;
  background: #eaeef3;
  border: 1px solid rgba(15, 23, 42, .08);
  border-radius: 9px;
  padding: 3px;
  gap: 2px;
}
.seg-btn {
  display: inline-flex; align-items: center; gap: 5px;
  border: none; background: none; cursor: pointer;
  font: inherit; font-size: 12.5px; color: var(--text-2);
  padding: 4px 12px; border-radius: 6px;
  transition: background .15s, color .15s, box-shadow .15s;
}
.seg-btn:hover { color: var(--text); }
.seg-btn.active {
  background: #fff; color: var(--text); font-weight: 650;
  box-shadow: 0 1px 3px rgba(15, 23, 42, .1);
}
.seg-count {
  font-size: 11px; color: var(--text-3);
  background: rgba(15, 23, 42, .06);
  padding: 0 6px; border-radius: 999px;
  font-variant-numeric: tabular-nums;
}
.seg-btn.active .seg-count {
  background: var(--accent-soft);
  color: var(--accent-strong);
}
.seg-label { font-size: 11.5px; color: var(--text-3); padding: 0 8px 0 6px; align-self: center; font-weight: 550; }

/* 状态胶囊 */
.pill {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 2px 10px; border-radius: 999px;
  font-size: 12px; font-weight: 600; line-height: 1.5;
}
.pill-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.pill-ok { background: var(--success-soft); color: var(--success); }
.pill-warn { background: var(--warn-soft); color: var(--warn); }

/* 桌面表格容器 */
.orders-panel {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--r);
  box-shadow: var(--shadow-sm);
  overflow: hidden;
}
.orders-panel .qz-pager {
  margin-top: 0;
  border: 0;
  border-top: 1px solid var(--border);
  border-radius: 0;
  background: var(--bg-subtle);
  box-shadow: none;
}
.orders-table-wrap { overflow: auto; max-height: calc(100vh - 360px); min-height: 280px; }
.orders-table thead th { position: sticky; top: 0; z-index: 2; }
.orders-table th.chk-col, .orders-table td.chk-col { width: 44px; padding-right: 0; }
.orders-table tbody tr.picked td { background: var(--accent-soft); }
.orders-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.orders-table thead th {
  text-align: left; font-weight: 650; color: var(--text-3); font-size: 12px;
  padding: 11px 16px; border-bottom: 1px solid var(--border); white-space: nowrap;
  background: var(--bg-soft);
}
.orders-table th.num, .orders-table td.num { text-align: right; }
.orders-table th.act-col, .orders-table td.act-col { text-align: right; white-space: nowrap; }
.orders-table th.sortable { cursor: pointer; user-select: none; }
.orders-table th.sortable:hover { color: var(--text); }
.sort-ind { color: var(--accent); font-weight: 700; }
.orders-table tbody td {
  padding: 11px 16px;
  border-bottom: 1px solid rgba(15, 23, 42, .055);
  color: var(--text-2);
  vertical-align: middle;
}
.orders-table tbody tr:last-child td { border-bottom: none; }
.orders-table tbody tr:hover td { background: var(--accent-subtle); }
.orders-table tbody tr.refunded td { color: var(--text-3); }
.orders-table td.id-col, .id-col { font-family: var(--mono); font-size: 12px; color: var(--text-3); }
.orders-table td.strong { color: var(--text); font-weight: 650; }
.user-cell { display: inline-flex; align-items: center; gap: 7px; font-weight: 550; color: var(--text); }
.user-cell.muted { color: var(--text-3); font-style: italic; font-weight: 400; }
.user-dot {
  width: 22px; height: 22px; border-radius: 50%;
  display: inline-grid; place-items: center;
  background: var(--bg-soft); border: 1px solid var(--border);
  font-size: 11px; font-weight: 700; font-style: normal; color: var(--text-2);
}
.orders-table td.time { color: var(--text-3); white-space: nowrap; font-variant-numeric: tabular-nums; font-size: 12.5px; }
.orders-table .amount { color: var(--text); font-weight: 700; font-size: 13.5px; font-variant-numeric: tabular-nums; }
.orders-table .amount-sub { display: block; margin-top: 2px; font-size: 11px; color: var(--warn); font-variant-numeric: tabular-nums; }
.orders-table .act-col :deep(.n-button) { margin-left: 6px; }

/* 分组表头行 */
.orders-table tbody tr.group-row td {
  background: var(--bg-soft);
  padding: 8px 16px;
  border-bottom: 1px solid var(--border);
  cursor: pointer;
  user-select: none;
}
.orders-table tbody tr.group-row:hover td { background: #eef2f7; }
.group-bar { display: flex; align-items: center; gap: 8px; }
.group-chev {
  display: inline-block; font-size: 12px; color: var(--text-3);
  transition: transform .18s ease;
}
.group-chev.closed { transform: rotate(-90deg); }
.group-name { font-weight: 700; font-size: 13px; color: var(--text); }
.group-badge {
  font-size: 11px; font-weight: 600; color: var(--accent-strong);
  background: var(--accent-soft); padding: 1px 8px; border-radius: 999px;
}
.group-meta { font-size: 12px; color: var(--text-3); font-variant-numeric: tabular-nums; margin-left: auto; }
.group-meta b { color: var(--text); }

/* 默认隐藏卡片，窄屏才显示 */
.orders-cards { display: none; }
.order-group + .order-group { margin-top: 14px; }
.order-group .group-head {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 4px 8px; border-bottom: 1px solid var(--border); margin-bottom: 10px;
  cursor: pointer;
}
@media (max-width: 720px) {
  .orders-panel { display: none; }
  .orders-cards { display: block; }
  .orders-toolbar .seg { flex-wrap: wrap; }
}
</style>
