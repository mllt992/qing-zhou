<template>
  <div>
    <h2 class="page-title">积分明细</h2>
    <p class="page-sub">收支一目了然，实时掌握积分动向</p>

    <PointRedeem @redeemed="reloadPoints" />

    <!-- ============ KPI 总览 ============ -->
    <div class="kpi-row">
      <div class="kpi-card">
        <div class="kpi-label">当前积分</div>
        <div class="kpi-value accent">{{ dBalance }}</div>
        <div class="kpi-sub">{{ yuan(dBalance) }}</div>
      </div>
      <div class="kpi-card">
        <div class="kpi-label">累计收入</div>
        <div class="kpi-value up">{{ dIncome }}</div>
        <div class="kpi-sub">{{ incomeCount }} 笔入账</div>
      </div>
      <div class="kpi-card">
        <div class="kpi-label">累计支出</div>
        <div class="kpi-value down">{{ dExpense }}</div>
        <div class="kpi-sub">{{ expenseCount }} 笔消费</div>
      </div>
      <div class="kpi-card">
        <div class="kpi-label">近 7 天净增</div>
        <div class="kpi-value" :class="net7 >= 0 ? 'up' : 'down'">{{ dNet7 > 0 ? '+' : '' }}{{ dNet7 }}</div>
        <div class="kpi-sub">入账 {{ dIn7 }} / 支出 {{ dOut7 }}</div>
      </div>
    </div>

    <!-- ============ 数据可视化 ============ -->
    <div class="two-col">
      <n-card size="small" class="sec">
        <template #header>
          <span class="sec-title">收支构成</span>
          <span class="sec-note">按类型统计</span>
        </template>
        <div ref="pieEl" class="chart" style="height:250px;" />
      </n-card>
      <n-card size="small" class="sec">
        <template #header>
          <span class="sec-title">近 30 天趋势</span>
          <span class="sec-note">每日收支净额</span>
        </template>
        <div ref="barEl" class="chart" style="height:250px;" />
      </n-card>
    </div>

    <!-- ============ 筛选交互 ============ -->
    <div class="filters">
      <n-radio-group v-model:value="typeFilter" size="small">
        <n-radio-button value="all">全部</n-radio-button>
        <n-radio-button value="in">收入</n-radio-button>
        <n-radio-button value="out">支出</n-radio-button>
      </n-radio-group>
      <n-select v-model:value="typeSel" size="small" clearable placeholder="按类型筛选"
                :options="typeOptions" style="width:170px;" />
      <n-input v-model:value="kw" size="small" clearable placeholder="搜索备注" style="width:170px;" />
      <span class="spacer" />
      <span class="muted">共 {{ filtered.length }} 笔</span>
    </div>

    <!-- ============ 明细列表 ============ -->
    <n-spin :show="loading">
      <div v-if="filtered.length" class="card-grid">
        <div v-for="(t, i) in filtered" :key="t.id" class="tx-card" :style="{ '--i': Math.min(i, 12) }">
          <div class="tx-ic" :class="t.amount > 0 ? 'up' : 'down'">
            <span>{{ t.amount > 0 ? '↑' : '↓' }}</span>
          </div>
          <div class="tx-main">
            <div class="tx-title">{{ typeLabel[t.type] || t.type || '—' }}</div>
            <div class="tx-meta">
              <span>{{ fmtDateTime(t.created_at) }}</span>
              <span v-if="t.note" class="tx-note">{{ t.note }}</span>
            </div>
          </div>
          <div class="tx-side">
            <div class="tx-amt" :class="t.amount > 0 ? 'up' : 'down'">{{ (t.amount > 0 ? '+' : '') + t.amount }}</div>
            <div class="tx-bal">余额 {{ t.balance_after }}</div>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" :description="txs.length ? '没有符合条件的明细' : '还没有积分收支记录'" style="padding:40px 0;">
        <template #extra>
          <div class="empty-actions">
            <span>{{ txs.length ? '当前筛选组合未命中记录，可清除条件查看全部明细。' : '充值、赠送、购买与退款都会在这里保留余额快照。' }}</span>
            <n-button v-if="txs.length" size="small" @click="resetFilters">清除筛选</n-button>
            <n-button v-else size="small" @click="router.push('/shop')">查看积分商城</n-button>
          </div>
        </template>
      </n-empty>
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import PointRedeem from '@/components/PointRedeem.vue'
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { NCard, NSpin, NEmpty, NButton, NRadioGroup, NRadioButton, NSelect, NInput } from 'naive-ui'
import * as echarts from 'echarts'
import { useAuthStore } from '@/stores/auth'
import { apiGet } from '@/api'
import { fmtDateTime, yuan } from '@/utils/format'
import { useCountUp } from '@/utils/countup'

const auth = useAuthStore()
const router = useRouter()
const txs = ref<any[]>([])
const loading = ref(false)
const typeFilter = ref('all')
const typeSel = ref<string | null>(null)
const kw = ref('')
function resetFilters() { typeFilter.value = 'all'; typeSel.value = null; kw.value = '' }

const typeLabel: Record<string, string> = {
  admin_recharge: '管理员充值', purchase: '购买消费', signup_bonus: '注册赠送',
  redeem: '兑换码到账', refund: '退款', adjust: '调整', admin_grant: '管理员赠送',
}

// 每笔记录附带本地化类型标签，方便按类型筛选
const txItems = computed(() => txs.value.map(t => ({
  ...t,
  label: typeLabel[t.type] || t.type || '其他',
})))

const typeOptions = computed(() => {
  const map = new Map<string, string>()
  for (const t of txs.value) {
    const k = t.type || ''
    if (!map.has(k)) map.set(k, typeLabel[k] || k || '其他')
  }
  return [...map.entries()].map(([value, label]) => ({ label, value }))
})

const filtered = computed(() => {
  let list = txItems.value
  if (typeFilter.value === 'in') list = list.filter(t => t.amount > 0)
  else if (typeFilter.value === 'out') list = list.filter(t => t.amount < 0)
  if (typeSel.value) list = list.filter(t => (t.type || '') === typeSel.value)
  const q = kw.value.trim().toLowerCase()
  if (q) list = list.filter(t => (t.note || '').toLowerCase().includes(q) || (t.label || '').toLowerCase().includes(q))
  return list
})

// ---- KPI 统计 ----
const income = computed(() => txItems.value.filter(t => t.amount > 0).reduce((s, t) => s + t.amount, 0))
const expense = computed(() => Math.abs(txItems.value.filter(t => t.amount < 0).reduce((s, t) => s + t.amount, 0)))
const incomeCount = computed(() => txItems.value.filter(t => t.amount > 0).length)
const expenseCount = computed(() => txItems.value.filter(t => t.amount < 0).length)

const balance = ref(0)
const net7 = ref(0)
const in7 = ref(0)
const out7 = ref(0)

// ---- 数字滚动动画（useCountUp 见 utils/countup.ts，全站共用同一条缓动）----
const dBalance = useCountUp(() => Math.round(balance.value))
const dIncome = useCountUp(() => Math.round(income.value))
const dExpense = useCountUp(() => Math.round(expense.value))
const dNet7 = useCountUp(() => Math.round(net7.value))
const dIn7 = useCountUp(() => Math.round(in7.value))
const dOut7 = useCountUp(() => Math.round(out7.value))

function computeRecent() {
  const now = Math.floor(Date.now() / 1000)
  const seven = now - 7 * 86400
  let ni = 0, no = 0
  for (const t of txs.value) {
    if ((t.created_at || 0) < seven) continue
    if (t.amount > 0) ni += t.amount
    else no += Math.abs(t.amount)
  }
  in7.value = ni; out7.value = no; net7.value = ni - no
}

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
  for (const t of txs.value) {
    if (!t.amount) continue
    const k = typeLabel[t.type] || t.type || '其他'
    map.set(k, (map.get(k) || 0) + Math.abs(t.amount))
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
  // 聚合最近 30 天每日收支净额（按本地时区日期分桶，与横轴标签一致）
  const days: { key: string; label: string; net: number; up: number; down: number }[] = []
  const dayIdx = new Map<string, number>()
  const now = new Date()
  now.setHours(0, 0, 0, 0)
  const p = (n: number) => String(n).padStart(2, '0')
  for (let i = 29; i >= 0; i--) {
    const d = new Date(now.getTime() - i * 86400000)
    const key = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
    dayIdx.set(key, days.length)
    days.push({ key, label: `${p(d.getMonth() + 1)}-${p(d.getDate())}`, net: 0, up: 0, down: 0 })
  }
  for (const t of txs.value) {
    const d = new Date((t.created_at || 0) * 1000)
    const key = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
    const i = dayIdx.get(key)
    if (i === undefined) continue
    if (t.amount > 0) days[i].up += t.amount
    else days[i].down += Math.abs(t.amount)
    days[i].net = days[i].up - days[i].down
  }
  const hasData = days.some(d => d.net !== 0)
  if (!hasData) return {
    title: { text: '近 30 天暂无收支', left: 'center', top: 'center', textStyle: { color: '#767676', fontSize: 13, fontWeight: 400 } },
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
        return `${d.label}<br/>收入 +${d.up}<br/>支出 -${d.down}<br/><b>净额 ${d.net >= 0 ? '+' : ''}${d.net}</b>`
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

async function reloadPoints() {
  balance.value = auth.user?.points || 0
  loading.value = true
  try {
    const data = await apiGet<any>('/api/user/points')
    txs.value = data?.transactions || []
    balance.value = data?.balance ?? auth.user?.points ?? 0
  } catch {} finally {
    loading.value = false
  }
  computeRecent()
  await nextTick()
  draw()
}
onMounted(async () => {
  await reloadPoints()
  window.addEventListener('resize', onResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  pie?.dispose(); pie = null
  bar?.dispose(); bar = null
})
</script>

<style scoped>
.page-title { font-size: 21px; margin-bottom: 4px; }
.page-sub { color: var(--text-2); margin-bottom: 18px; font-size: 13px; }

/* KPI 卡片 */
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; margin-bottom: 16px; }
.kpi-card {
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 14px 16px; animation: riseIn .5s cubic-bezier(.22,1,.36,1) backwards;
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

/* 区块 */
.sec { margin-bottom: 14px; border-radius: var(--r-sm); }
.sec-title { font-weight: 650; font-size: 14px; }
.sec-note { font-size: 11.5px; color: var(--text-3); margin-left: 10px; font-weight: 400; }
.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 14px; }
.chart { width: 100%; }

/* 筛选栏 */
.filters { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.filters .spacer { flex: 1; }
.muted { color: var(--text-3); font-size: 12px; }
.empty-actions { display:flex; flex-direction:column; align-items:center; gap:10px; max-width:420px; color:var(--text-3); font-size:12px; line-height:1.6; }

/* 明细卡片 */
.card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 12px;
}
@media (max-width: 640px) { .card-grid { grid-template-columns: 1fr; gap: 10px; } }

.tx-card {
  display: flex; align-items: flex-start; gap: 12px;
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 13px 14px;
  animation: riseIn .45s cubic-bezier(.22,1,.36,1) backwards;
  animation-delay: calc(var(--i, 0) * 40ms);
  transition: box-shadow .18s ease, transform .18s ease, border-color .18s ease;
}
.tx-card:hover { box-shadow: var(--shadow); transform: translateY(-2px); border-color: #d5d5d5; }

.tx-ic {
  width: 34px; height: 34px; border-radius: 10px; flex-shrink: 0;
  display: grid; place-items: center; font-size: 15px; font-weight: 750;
}
.tx-ic.up { background: #eef4ef; color: #4d7256; }
.tx-ic.down { background: #f9eeec; color: #a8564b; }

.tx-main { flex: 1; min-width: 0; }
.tx-title { font-weight: 650; font-size: 14px; color: var(--text); }
.tx-meta { display: flex; flex-wrap: wrap; gap: 4px 12px; font-size: 12px; color: var(--text-3); margin-top: 3px; }
.tx-note { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%; }

.tx-side { text-align: right; flex-shrink: 0; }
.tx-amt { font-weight: 720; font-size: 15px; font-variant-numeric: tabular-nums; }
.tx-amt.up { color: #4d7256; }
.tx-amt.down { color: #a8564b; }
.tx-bal { font-size: 11.5px; color: var(--text-3); margin-top: 3px; font-variant-numeric: tabular-nums; }

@media (max-width: 900px) { .two-col { grid-template-columns: 1fr; } }
</style>
