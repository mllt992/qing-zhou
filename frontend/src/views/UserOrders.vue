<template>
  <div>
    <!-- 页面头 -->
    <div class="ord-head">
      <div>
        <h2 class="page-title" style="margin-bottom:4px;">订单记录</h2>
        <p class="page-sub">消费一目了然，订单轨迹清晰可见</p>
      </div>
      <n-button size="small" secondary @click="router.push('/shop')">
        <template #icon><n-icon><CartOutline /></n-icon></template>
        去商城
      </n-button>
    </div>

    <!-- ============ KPI 总览 ============ -->
    <div class="kpi-row">
      <div class="kpi-card">
        <div class="kpi-label">累计消费</div>
        <div class="kpi-value accent">{{ dTotalSpend }}</div>
        <div class="kpi-sub">{{ yuan(totalSpend) }} · {{ successCount }} 笔成功</div>
      </div>
      <div class="kpi-card">
        <div class="kpi-label">订单总数</div>
        <div class="kpi-value">{{ orders.length }}</div>
        <div class="kpi-sub">成功 {{ successCount }} · 退款 {{ refundedCount }}</div>
      </div>
      <div class="kpi-card">
        <div class="kpi-label">已退款</div>
        <div class="kpi-value down">{{ refundedCount }}</div>
        <div class="kpi-sub">退回 {{ refundedPoints }} 积分</div>
      </div>
      <div class="kpi-card">
        <div class="kpi-label">近 30 天消费</div>
        <div class="kpi-value up">{{ dMonthSpend }}</div>
        <div class="kpi-sub">{{ monthOrders }} 笔订单</div>
      </div>
    </div>

    <!-- ============ 数据可视化 ============ -->
    <div class="two-col">
      <n-card size="small" class="sec">
        <template #header>
          <span class="sec-title">消费构成</span>
          <span class="sec-note">按商品类型统计</span>
        </template>
        <div ref="pieEl" class="chart" style="height:240px;" />
      </n-card>
      <n-card size="small" class="sec">
        <template #header>
          <span class="sec-title">近 30 天消费趋势</span>
          <span class="sec-note">每日订单金额</span>
        </template>
        <div ref="barEl" class="chart" style="height:240px;" />
      </n-card>
    </div>

    <!-- ============ 筛选交互 ============ -->
    <div class="filters">
      <n-radio-group v-model:value="statusFilter" size="small">
        <n-radio-button value="all">全部</n-radio-button>
        <n-radio-button value="success">成功</n-radio-button>
        <n-radio-button value="refunded">已退款</n-radio-button>
      </n-radio-group>
      <n-select v-model:value="typeSel" size="small" clearable placeholder="按类型筛选"
                :options="typeOptions" style="width:150px;" />
      <n-input v-model:value="kw" size="small" clearable placeholder="搜索套餐" style="width:160px;" />
      <span class="spacer" />
      <span class="muted">共 {{ filtered.length }} 单</span>
    </div>

    <!-- ============ 订单时间线 ============ -->
    <n-spin :show="loading">
      <div v-if="filtered.length" class="order-list">
        <template v-for="g in grouped" :key="g.key">
          <div class="group-head">
            <span class="group-dot" />
            <span class="group-name">{{ g.key }}</span>
            <span class="group-meta">{{ g.count }} 单 · {{ g.revenue }} 积分</span>
          </div>
          <div v-for="(o, i) in g.items" :key="o.id" class="order-item" :style="{ '--i': Math.min(i, 12) }">
            <div class="oi-ic" :class="typeCls(o.type)">
              <span>{{ typeIcon(o.type) }}</span>
            </div>
            <div class="oi-main">
              <div class="oi-title">{{ o.name || '—' }}</div>
              <div class="oi-meta">
                <span class="pill" :class="o.status === 'success' ? 'pill-ok' : 'pill-warn'">{{ o.status === 'success' ? '成功' : '已退款' }}</span>
                <span class="oi-type">{{ typeLabel(o.type) }}</span>
                <span class="oi-time" :title="fmtDateTime(o.created_at)">{{ timeAgo(o.created_at) }}</span>
              </div>
            </div>
            <div class="oi-side">
              <div class="oi-amt" :class="o.status === 'refunded' ? 'down' : ''">
                {{ o.price_points }}<span class="oi-unit">积分</span>
              </div>
              <div v-if="o.status === 'refunded'" class="oi-refund">
                退 {{ o.refunded_points }}<template v-if="o.refund_ratio > 0 && o.refund_ratio < 1">（{{ Math.round(o.refund_ratio * 100) }}%）</template>
              </div>
            </div>
          </div>
        </template>
        <div v-if="filtered.length > pageSize" class="qz-pager">
          <span class="qz-pager-info">
            显示第 <b>{{ (page - 1) * pageSize + 1 }}–{{ Math.min(filtered.length, page * pageSize) }}</b> 单，共 <b>{{ filtered.length }}</b> 单
          </span>
          <n-pagination v-model:page="page" v-model:page-size="pageSize" :item-count="filtered.length" :page-sizes="[15, 30, 60]" show-size-picker size="small" />
        </div>
      </div>
      <n-empty v-else-if="!loading" :description="orders.length ? '没有符合条件的订单' : '暂无订单'" style="padding:40px 0;">
        <template #extra>
          <div class="empty-actions">
            <span>{{ orders.length ? '当前状态、类型或关键词组合没有命中订单。' : '购买套餐或流量包后，会在这里记录价格、状态和退款明细。' }}</span>
            <n-button v-if="orders.length" size="small" @click="resetFilters">清除筛选</n-button>
            <n-button v-else size="small" @click="router.push('/shop')">去商城看看</n-button>
          </div>
        </template>
      </n-empty>
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { NCard, NSpin, NEmpty, NButton, NRadioGroup, NRadioButton, NSelect, NInput, NIcon, NPagination } from 'naive-ui'
import { CartOutline } from '@vicons/ionicons5'
import * as echarts from 'echarts'
import { apiList } from '@/api'
import { fmtDateTime, timeAgo, yuan } from '@/utils/format'
import { useCountUp } from '@/utils/countup'

const router = useRouter()
const orders = ref<any[]>([])
const loading = ref(false)
const statusFilter = ref('all')
const typeSel = ref<string | null>(null)
const kw = ref('')
const page = ref(1)
const pageSize = ref(15)
watch([statusFilter, typeSel, kw, pageSize], () => { page.value = 1 })
function resetFilters() { statusFilter.value = 'all'; typeSel.value = null; kw.value = '' }

// ---- 类型元信息 ----
const typeMeta: Record<string, { label: string; cls: string; ch: string }> = {
  plan: { label: '订阅计划', cls: 'plan', ch: '计' },
  traffic: { label: '流量包', cls: 'traffic', ch: '流' },
}
function typeLabel(t: string) { return typeMeta[t]?.label || t || '—' }
function typeCls(t: string) { return typeMeta[t]?.cls || 'other' }
function typeIcon(t: string) { return typeMeta[t]?.ch || '购' }

// 净支出口径：成功全额计，退款按留存部分计（与 AdminOrders 保持一致）
function retained(o: any): number {
  if (o.status === 'refunded') return (o.price_points || 0) - (o.refunded_points || 0)
  return o.price_points || 0
}

const typeOptions = computed(() => {
  const map = new Map<string, string>()
  for (const o of orders.value) {
    if (!map.has(o.type)) map.set(o.type, typeLabel(o.type))
  }
  return [...map.entries()].map(([value, label]) => ({ label, value }))
})

const filtered = computed(() => {
  let list = orders.value
  if (statusFilter.value !== 'all') list = list.filter(o => o.status === statusFilter.value)
  if (typeSel.value) list = list.filter(o => (o.type || '') === typeSel.value)
  const q = kw.value.trim().toLowerCase()
  if (q) list = list.filter(o => (o.name || '').toLowerCase().includes(q))
  return list
})

// ---- KPI 统计 ----
const successCount = computed(() => orders.value.filter(o => o.status === 'success').length)
const refundedCount = computed(() => orders.value.filter(o => o.status === 'refunded').length)
const refundedPoints = computed(() => orders.value.reduce((s, o) => s + (o.refunded_points || 0), 0))
const totalSpend = computed(() => orders.value.reduce((s, o) => s + retained(o), 0))

const monthSpend = computed(() => {
  const cutoff = Math.floor(Date.now() / 1000) - 30 * 86400
  return orders.value.filter(o => (o.created_at || 0) >= cutoff).reduce((s, o) => s + retained(o), 0)
})
const monthOrders = computed(() => {
  const cutoff = Math.floor(Date.now() / 1000) - 30 * 86400
  return orders.value.filter(o => (o.created_at || 0) >= cutoff).length
})

// ---- 数字滚动动画（与积分明细页共用 utils/countup.ts）----
const dTotalSpend = useCountUp(() => Math.round(totalSpend.value))
const dMonthSpend = useCountUp(() => Math.round(monthSpend.value))

// ---- 按相对时间分组的订单时间线 ----
function bucketLabel(o: any): string {
  const ts = o.created_at || 0
  const now = Math.floor(Date.now() / 1000)
  const day = 86400
  const startOfToday = Math.floor(new Date(new Date().setHours(0, 0, 0, 0)).getTime() / 1000)
  if (ts >= startOfToday) return '今天'
  if (ts >= startOfToday - day) return '昨天'
  if (ts >= now - 7 * day) return '7 天内'
  if (ts >= now - 30 * day) return '30 天内'
  return '更早'
}
const bucketOrder = ['今天', '昨天', '7 天内', '30 天内', '更早']

const grouped = computed(() => {
  const start = (page.value - 1) * pageSize.value
  const slice = filtered.value.slice(start, start + pageSize.value)
  const map = new Map<string, any[]>()
  for (const o of slice) {
    const k = bucketLabel(o)
    if (!map.has(k)) map.set(k, [])
    map.get(k)!.push(o)
  }
  return bucketOrder
    .filter(k => map.has(k))
    .map(k => ({
      key: k,
      count: map.get(k)!.length,
      revenue: map.get(k)!.reduce((s, o) => s + retained(o), 0),
      items: map.get(k)!,
    }))
})

// ---- ECharts ----
const pieEl = ref<HTMLElement | null>(null)
const barEl = ref<HTMLElement | null>(null)
let pie: echarts.ECharts | null = null
let bar: echarts.ECharts | null = null

const PIE = ['#6f8f76', '#5e7a99', '#bf9540', '#c2685c', '#8d7fa8', '#7f9ea8', '#a89a7f', '#9aa0a6']
const C = { up: '#6f8f76', down: '#c2685c', gold: '#bf9540', info: '#5e7a99' }

const axisStyle = {
  axisLine: { lineStyle: { color: '#e5e5e5' } },
  axisTick: { show: false },
  axisLabel: { color: '#767676', fontSize: 11 },
}

function pieOption() {
  const map = new Map<string, number>()
  for (const o of orders.value) {
    const v = retained(o)
    if (v <= 0) continue
    const k = typeLabel(o.type)
    map.set(k, (map.get(k) || 0) + v)
  }
  const data = [...map.entries()]
    .map(([name, value], i) => ({ name, value, itemStyle: { color: PIE[i % PIE.length] } }))
    .sort((a, b) => b.value - a.value)
  if (!data.length) return {
    title: { text: '暂无数据', left: 'center', top: 'center', textStyle: { color: '#767676', fontSize: 13, fontWeight: 400 } },
    series: [],
  }
  return {
    tooltip: { trigger: 'item', formatter: (p: any) => `${p.name}<br/>${p.value} 积分 (${p.percent}%)` },
    legend: { type: 'scroll', orient: 'vertical', right: 0, top: 'center',
      textStyle: { color: '#595959', fontSize: 11 }, itemWidth: 9, itemHeight: 9, icon: 'roundRect' },
    series: [{
      type: 'pie', radius: ['45%', '70%'], center: ['35%', '50%'], avoidLabelOverlap: true,
      itemStyle: { borderColor: '#fff', borderWidth: 2 },
      label: { show: false }, labelLine: { show: false },
      data,
    }],
  }
}

function barOption() {
  // 聚合最近 30 天每日净消费（退款部分从当日金额中扣除）
  const days: { key: string; label: string; net: number; refund: number }[] = []
  const dayIdx = new Map<string, number>()
  const now = new Date()
  now.setHours(0, 0, 0, 0)
  const p = (n: number) => String(n).padStart(2, '0')
  for (let i = 29; i >= 0; i--) {
    const d = new Date(now.getTime() - i * 86400000)
    const key = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
    dayIdx.set(key, days.length)
    days.push({ key, label: `${p(d.getMonth() + 1)}-${p(d.getDate())}`, net: 0, refund: 0 })
  }
  for (const o of orders.value) {
    const d = new Date((o.created_at || 0) * 1000)
    const key = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
    const i = dayIdx.get(key)
    if (i === undefined) continue
    days[i].net += retained(o)
    if (o.status === 'refunded') days[i].refund += o.refunded_points || 0
  }
  const hasData = days.some(d => d.net !== 0 || d.refund !== 0)
  if (!hasData) return {
    title: { text: '近 30 天暂无消费', left: 'center', top: 'center', textStyle: { color: '#767676', fontSize: 13, fontWeight: 400 } },
    grid: { left: 8, right: 12, top: 20, bottom: 4, containLabel: true },
    xAxis: { type: 'category', data: days.map(d => d.label), ...axisStyle },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle, axisLine: { show: false } },
    series: [],
  }
  return {
    grid: { left: 8, right: 12, top: 20, bottom: 4, containLabel: true },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      formatter: (ps: any[]) => {
        if (!ps.length) return ''
        const d = days[ps[0].dataIndex]
        return `${d.label}<br/>消费 ${d.net}<br/>${d.refund ? `退款 -${d.refund}` : ''}`
      },
    },
    xAxis: { type: 'category', data: days.map(d => d.label), ...axisStyle },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle, axisLine: { show: false } },
    series: [{
      type: 'bar', barMaxWidth: 10,
      data: days.map(d => ({
        value: d.net,
        itemStyle: { color: d.net >= 0 ? C.up : C.down, borderRadius: d.net >= 0 ? [3, 3, 0, 0] : [0, 0, 3, 3] },
      })),
    }],
  }
}

function draw() {
  if (pieEl.value && pieEl.value.clientWidth) {
    if (!pie) pie = echarts.init(pieEl.value)
    pie.setOption(pieOption(), true)
  }
  if (barEl.value && barEl.value.clientWidth) {
    if (!bar) bar = echarts.init(barEl.value)
    bar.setOption(barOption(), true)
  }
}

function onResize() { pie?.resize(); bar?.resize() }

onMounted(async () => {
  loading.value = true
  try { orders.value = await apiList('/api/user/orders') } catch {} finally { loading.value = false }
  await nextTick()
  draw()
  window.addEventListener('resize', onResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  pie?.dispose(); pie = null
  bar?.dispose(); bar = null
})
</script>

<style scoped>
.ord-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
.page-sub { color: var(--text-2); margin-bottom: 0; font-size: 13px; }

/* ---- KPI 卡片 ---- */
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; margin-bottom: 16px; }
.kpi-card {
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 14px 16px;
  animation: riseIn .5s cubic-bezier(.22,1,.36,1) backwards;
}
.kpi-card:nth-child(1) { animation-delay: 0ms; }
.kpi-card:nth-child(2) { animation-delay: 60ms; }
.kpi-card:nth-child(3) { animation-delay: 120ms; }
.kpi-card:nth-child(4) { animation-delay: 180ms; }
@keyframes riseIn { from { opacity: 0; transform: translateY(12px); } to { opacity: 1; transform: none; } }
.kpi-label { font-size: 12px; color: var(--text-3); font-weight: 550; }
.kpi-value { font-size: 24px; font-weight: 720; letter-spacing: -0.02em; margin-top: 6px; line-height: 1.15; font-variant-numeric: tabular-nums; }
.kpi-value.accent { color: var(--text); }
.kpi-value.up { color: #4d7256; }
.kpi-value.down { color: #a8564b; }
.kpi-sub { font-size: 11.5px; color: var(--text-3); margin-top: 2px; }

/* ---- 图表区块 ---- */
.sec { margin-bottom: 14px; border-radius: var(--r-sm); }
.sec-title { font-weight: 650; font-size: 14px; }
.sec-note { font-size: 11.5px; color: var(--text-3); margin-left: 10px; font-weight: 400; }
.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 14px; }
.chart { width: 100%; }

/* ---- 筛选栏 ---- */
.filters { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 14px; }
.filters .spacer { flex: 1; }
.muted { color: var(--text-3); font-size: 12px; }
.empty-actions { display:flex; flex-direction:column; align-items:center; gap:10px; max-width:420px; color:var(--text-3); font-size:12px; line-height:1.6; }

/* ---- 订单时间线 ---- */
.order-list { display: flex; flex-direction: column; }
.group-head {
  display: flex; align-items: center; gap: 8px;
  margin: 10px 0 8px; padding: 0 2px;
  animation: riseIn .4s cubic-bezier(.22,1,.36,1) backwards;
}
.group-head:first-child { margin-top: 0; }
.group-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); flex-shrink: 0; }
.group-name { font-weight: 700; font-size: 13.5px; color: var(--text); letter-spacing: -0.01em; }
.group-meta { font-size: 11.5px; color: var(--text-3); font-variant-numeric: tabular-nums; }

.order-item {
  display: flex; align-items: center; gap: 12px;
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 12px 14px; margin-bottom: 8px;
  animation: riseIn .45s cubic-bezier(.22,1,.36,1) backwards;
  animation-delay: calc(var(--i, 0) * 45ms);
  transition: box-shadow .18s ease, transform .18s ease, border-color .18s ease;
}
.order-item:hover { box-shadow: var(--shadow); transform: translateY(-2px); border-color: #d5d5d5; }

.oi-ic {
  width: 38px; height: 38px; border-radius: 11px; flex-shrink: 0;
  display: grid; place-items: center; font-size: 15px; font-weight: 750;
}
.oi-ic.plan { background: #eef4ef; color: #4d7256; }
.oi-ic.traffic { background: #eef1f5; color: #4a6a88; }
.oi-ic.other { background: #f6f1e7; color: #a17a2e; }
.oi-ic.other { background: #f2f2f2; color: var(--text-2); }

.oi-main { flex: 1; min-width: 0; }
.oi-title { font-weight: 650; font-size: 14px; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.oi-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; margin-top: 4px; }
.oi-type { font-size: 11.5px; color: var(--text-3); }
.oi-time { font-size: 11.5px; color: var(--text-3); }

.oi-side { text-align: right; flex-shrink: 0; }
.oi-amt { font-weight: 720; font-size: 16px; font-variant-numeric: tabular-nums; color: var(--text); }
.oi-amt.down { color: #a8564b; text-decoration: line-through; text-decoration-color: rgba(168,86,75,.45); }
.oi-unit { font-size: 11px; font-weight: 500; color: var(--text-3); margin-left: 3px; }
.oi-refund { font-size: 11.5px; color: var(--warn); margin-top: 2px; font-variant-numeric: tabular-nums; }

/* 状态胶囊 */
.pill { display: inline-flex; align-items: center; padding: 1px 9px; border-radius: 999px; font-size: 11.5px; font-weight: 600; line-height: 1.6; }
.pill-ok { background: rgba(16,185,129,.12); color: #0f9d6f; }
.pill-warn { background: rgba(191,149,64,.15); color: var(--warn); }

@media (max-width: 900px) { .two-col { grid-template-columns: 1fr; } }
</style>
