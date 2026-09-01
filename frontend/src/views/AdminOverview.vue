<template>
  <div>
    <div class="ov-head">
      <div>
        <h2 class="page-title" style="margin-bottom:2px;">管理概览</h2>
        <p class="page-sub">系统运营数据一览</p>
      </div>
      <!-- 时间范围是全页的统一维度：KPI 的环比、流量趋势、用户表的「区间流量」
           都跟着它走，避免几块数据各说各的时间口径。 -->
      <n-radio-group v-model:value="range" size="small" @update:value="reload">
        <n-radio-button v-for="r in ranges" :key="r.v" :value="r.v">{{ r.l }}</n-radio-button>
      </n-radio-group>
    </div>

    <!-- KPI -->
    <div class="kpi-row">
      <div v-for="k in kpis" :key="k.key" class="kpi" :class="{ clickable: k.onClick }" @click="k.onClick?.()">
        <div class="kpi-top">
          <span class="kpi-label">{{ k.label }}</span>
          <span v-if="k.delta !== null" class="kpi-delta" :class="deltaClass(k.delta, k.goodUp)">
            {{ k.delta > 0 ? '↑' : k.delta < 0 ? '↓' : '–' }}{{ Math.abs(k.delta).toFixed(0) }}%
          </span>
        </div>
        <div class="kpi-value">{{ k.value }}</div>
        <div class="kpi-sub">{{ k.sub }}</div>
        <div v-if="k.spark?.length" class="kpi-spark"><Spark :data="k.spark" :color="k.color" /></div>
      </div>
    </div>

    <!-- 在线用户（点「当前在线」卡片展开） -->
    <n-card v-if="showOnline" size="small" class="sec" title="在线用户">
      <div v-if="!onlineUsers.length" class="empty">当前无人在线</div>
      <div v-else class="online-grid">
        <div v-for="u in onlineUsers" :key="u.name" class="online-item">
          <span class="dot" /><span class="on-name">{{ u.name }}</span>
          <span class="on-time">{{ timeAgo(u.value) }}</span>
        </div>
      </div>
    </n-card>

    <n-tabs v-model:value="tab" type="line" animated class="ov-tabs">
      <!-- ========== 趋势 ========== -->
      <n-tab-pane name="trend" tab="趋势">
        <n-card size="small" class="sec">
          <template #header>
            <span class="sec-title">流量趋势</span>
            <span class="sec-note">近 {{ days }} 天 · 上行 {{ fmtBytes(sumUp) }} / 下行 {{ fmtBytes(sumDown) }}</span>
          </template>
          <div ref="trafficEl" class="chart" style="height:280px;" />
        </n-card>

        <div class="two-col">
          <n-card size="small" class="sec">
            <template #header><span class="sec-title">积分收支</span><span class="sec-note">近 {{ days }} 天</span></template>
            <div ref="revenueEl" class="chart" style="height:240px;" />
          </n-card>
          <n-card size="small" class="sec">
            <template #header><span class="sec-title">新增注册</span><span class="sec-note">近 {{ days }} 天</span></template>
            <div ref="regEl" class="chart" style="height:240px;" />
          </n-card>
        </div>

        <n-card size="small" class="sec" title="用户状态分布">
          <div class="dist-row">
            <button v-for="d in distItems" :key="d.key" class="dist-item" @click="drillUsers(d.filter)">
              <span class="dist-val" :style="{ color: d.color }">{{ d.value }}</span>
              <span class="dist-label">{{ d.label }}</span>
            </button>
          </div>
          <p class="hint">点任意一项跳到「用户分析」并自动套用该筛选。</p>
        </n-card>
      </n-tab-pane>

      <!-- ========== 套餐分析 ========== -->
      <n-tab-pane name="package" tab="套餐分析">
        <div class="two-col">
          <n-card size="small" class="sec">
            <template #header><span class="sec-title">套餐收入占比</span><span class="sec-note">累计成功订单</span></template>
            <div ref="pkgPieEl" class="chart" style="height:260px;" />
          </n-card>
          <n-card size="small" class="sec">
            <template #header><span class="sec-title">套餐流量消耗</span><span class="sec-note">该套餐所有份额累计</span></template>
            <div ref="pkgBarEl" class="chart" style="height:260px;" />
          </n-card>
        </div>

        <n-card size="small" class="sec">
          <template #header>
            <span class="sec-title">套餐明细</span>
            <span class="sec-note">共 {{ packages.length }} 个（含已下架 / 已删除但有历史订单的）</span>
          </template>
          <div class="tbl-wrap">
            <table class="tbl">
              <thead>
                <tr>
                  <th class="l">套餐</th>
                  <th v-for="c in pkgCols" :key="c.k" :class="['sortable', { on: pkgSort === c.k }]" @click="sortPkg(c.k)">
                    {{ c.l }}<span v-if="pkgSort === c.k">{{ pkgDesc ? ' ↓' : ' ↑' }}</span>
                  </th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in sortedPackages" :key="p.package_id">
                  <td class="l">
                    <span class="pkg-name">{{ p.name }}</span>
                    <n-tag v-if="!p.enabled" size="tiny" :bordered="false" style="margin-left:6px;">已下架</n-tag>
                  </td>
                  <td>{{ p.orders }}</td>
                  <td><b>{{ p.revenue }}</b></td>
                  <td>{{ p.holders }}</td>
                  <td>{{ p.active_buckets }} / {{ p.buckets }}</td>
                  <td>{{ fmtBytes(p.traffic) }}</td>
                  <td>{{ p.buckets ? fmtBytes(Math.round(p.traffic / p.buckets)) : '—' }}</td>
                  <td>
                    <template v-if="p.quota > 0">
                      <div class="mini-bar"><i :style="{ width: Math.min(100, p.traffic / p.quota * 100) + '%' }" /></div>
                      <span class="muted">{{ (p.traffic / p.quota * 100).toFixed(0) }}%</span>
                    </template>
                    <span v-else class="muted">—</span>
                  </td>
                  <td :class="{ warn: p.expiring_7d > 0 }">{{ p.expiring_7d || '—' }}</td>
                  <td><n-button size="tiny" quaternary @click="drillUsers({ package_id: p.package_id })">看用户</n-button></td>
                </tr>
                <tr v-if="!packages.length"><td :colspan="pkgCols.length + 2" class="empty">暂无套餐数据</td></tr>
              </tbody>
            </table>
          </div>
        </n-card>
      </n-tab-pane>

      <!-- ========== 用量分析 ========== -->
      <!-- 自带时间口径（含「累计」与自定义区间），所以刻意不受页头 range 影响：
           页头那个是「近 N 天」的运营视角，这里要能回答任意区间与全生命周期。 -->
      <n-tab-pane name="usage" tab="用量分析" display-directive="show:lazy">
        <AdminUsageReport ref="usageReport" />
      </n-tab-pane>

      <!-- ========== 用户分析 ========== -->
      <n-tab-pane name="user" tab="用户分析">
        <n-card size="small" class="sec">
          <div class="filters">
            <n-input v-model:value="uf.q" size="small" clearable placeholder="搜索用户名 / 邮箱" style="width:190px;" @update:value="debouncedUsers" />
            <n-select v-model:value="uf.status" size="small" clearable placeholder="状态" style="width:120px;"
                      :options="[{label:'正常',value:'active'},{label:'封禁',value:'banned'}]" @update:value="loadUsers" />
            <n-select v-model:value="uf.package_id" size="small" clearable placeholder="套餐" style="width:170px;"
                      :options="pkgOptions" @update:value="loadUsers" />
            <n-select v-model:value="uf.expiry" size="small" clearable placeholder="到期" style="width:130px;"
                      :options="[{label:'未过期',value:'active'},{label:'7天内到期',value:'expiring_7d'},{label:'已过期',value:'expired'}]"
                      @update:value="loadUsers" />
            <n-checkbox v-model:checked="uf.online" @update:checked="loadUsers">仅在线</n-checkbox>
            <span class="spacer" />
            <span class="muted">共 {{ userTotal }} 人</span>
            <n-button v-if="filterActive" size="tiny" quaternary @click="clearFilters">清除筛选</n-button>
          </div>

          <n-spin :show="loadingUsers">
            <div class="tbl-wrap">
              <table class="tbl">
                <thead>
                  <tr>
                    <th class="l sortable" :class="{ on: uf.sort === 'username' }" @click="sortUser('username')">用户</th>
                    <th class="l">套餐</th>
                    <th class="sortable" :class="{ on: uf.sort === 'range_traffic' }" @click="sortUser('range_traffic')">
                      {{ days }}天流量<span v-if="uf.sort === 'range_traffic'">{{ uf.desc ? ' ↓' : ' ↑' }}</span>
                    </th>
                    <th class="sortable" :class="{ on: uf.sort === 'traffic' }" @click="sortUser('traffic')">
                      累计流量<span v-if="uf.sort === 'traffic'">{{ uf.desc ? ' ↓' : ' ↑' }}</span>
                    </th>
                    <th>额度</th>
                    <th class="sortable" :class="{ on: uf.sort === 'expiry' }" @click="sortUser('expiry')">
                      到期<span v-if="uf.sort === 'expiry'">{{ uf.desc ? ' ↓' : ' ↑' }}</span>
                    </th>
                    <th class="sortable" :class="{ on: uf.sort === 'last_online' }" @click="sortUser('last_online')">
                      最后在线<span v-if="uf.sort === 'last_online'">{{ uf.desc ? ' ↓' : ' ↑' }}</span>
                    </th>
                    <th class="sortable" :class="{ on: uf.sort === 'spend' }" @click="sortUser('spend')">
                      消费<span v-if="uf.sort === 'spend'">{{ uf.desc ? ' ↓' : ' ↑' }}</span>
                    </th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  <template v-for="u in users" :key="u.id">
                    <tr :class="{ open: expanded === u.id }">
                      <td class="l">
                        <span class="dot" :class="{ off: !isOnline(u) }" />
                        <span class="u-name">{{ u.username }}</span>
                        <n-tag v-if="u.status === 'banned'" size="tiny" type="error" :bordered="false" style="margin-left:6px;">封禁</n-tag>
                      </td>
                      <td class="l muted">{{ u.packages || '—' }}</td>
                      <td><b>{{ fmtBytes(u.range_traffic) }}</b></td>
                      <td>{{ fmtBytes(u.traffic) }}</td>
                      <td>
                        <template v-if="u.traffic_limit > 0">
                          <div class="mini-bar"><i :class="{ hot: u.traffic / u.traffic_limit > 0.9 }"
                                                   :style="{ width: Math.min(100, u.traffic / u.traffic_limit * 100) + '%' }" /></div>
                          <span class="muted">{{ fmtTotal(u.traffic_limit) }}</span>
                        </template>
                        <span v-else class="muted">0 B</span>
                      </td>
                      <td :class="expiryClass(u.expiry_at)">{{ u.expiry_at ? fmtDate(u.expiry_at) : (u.traffic_limit > 0 ? '永久' : '无套餐') }}</td>
                      <td class="muted">{{ u.last_online_at ? timeAgo(u.last_online_at) : '—' }}</td>
                      <td>{{ u.spend || 0 }}</td>
                      <td><n-button size="tiny" quaternary @click="toggleUser(u)">{{ expanded === u.id ? '收起' : '趋势' }}</n-button></td>
                    </tr>
                    <tr v-if="expanded === u.id" :key="u.id + '-d'" class="drill">
                      <td :colspan="9">
                        <div :ref="el => setUserChartEl(u.id, el)" class="chart" style="height:170px;" />
                      </td>
                    </tr>
                  </template>
                  <tr v-if="!users.length && !loadingUsers"><td colspan="9" class="empty">没有符合条件的用户</td></tr>
                </tbody>
              </table>
            </div>
          </n-spin>

          <div v-if="userTotal > pageSize" class="pager">
            <n-button size="tiny" :disabled="page === 0" @click="goPage(page - 1)">上一页</n-button>
            <span class="muted">{{ page + 1 }} / {{ Math.ceil(userTotal / pageSize) }}</span>
            <n-button size="tiny" :disabled="(page + 1) * pageSize >= userTotal" @click="goPage(page + 1)">下一页</n-button>
          </div>
        </n-card>
      </n-tab-pane>
    </n-tabs>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive, onMounted, onUnmounted, nextTick, watch, h, defineComponent } from 'vue'
import { NCard, NTabs, NTabPane, NRadioGroup, NRadioButton, NInput, NSelect, NCheckbox, NButton, NTag, NSpin, useMessage } from 'naive-ui'
import * as echarts from 'echarts'
import { apiGet, apiList } from '@/api'
import { fmtBytes, fmtTotal, fmtDate, timeAgo } from '@/utils/format'
import AdminUsageReport from '@/components/AdminUsageReport.vue'

const message = useMessage()
const usageReport = ref<any>(null)

// 图表配色沿用全站的暖中性色板（global.css 的 --info/--warn 等），不引入高饱和色。
const C = { up: '#6f8f76', down: '#5e7a99', gold: '#bf9540', red: '#c2685c', gray: '#9aa0a6' }
const PIE = ['#5e7a99', '#6f8f76', '#bf9540', '#c2685c', '#8d7fa8', '#7f9ea8', '#a89a7f', '#9aa0a6']

const ranges = [{ v: '7d', l: '7天' }, { v: '14d', l: '14天' }, { v: '30d', l: '30天' }, { v: '90d', l: '90天' }]
const range = ref('14d')
const days = computed(() => ({ '7d': 7, '14d': 14, '30d': 30, '90d': 90 } as any)[range.value] || 14)
const tab = ref('trend')

const ov = ref<any>({})
const onlineUsers = ref<any[]>([])
const showOnline = ref(false)
const trafficData = ref<any[]>([])
const revenueData = ref<any[]>([])
const regData = ref<any[]>([])
const dist = ref<any>({})
const packages = ref<any[]>([])

const sumUp = computed(() => trafficData.value.reduce((s, d) => s + (d.up || 0), 0))
const sumDown = computed(() => trafficData.value.reduce((s, d) => s + (d.down || 0), 0))

// ---- KPI ----
function pctDelta(cur: number, prev: number): number | null {
  // 上期为 0 时百分比没有意义（除以零，或者「从 0 涨到 5」算无穷大），
  // 宁可不显示环比，也不显示一个骗人的数字。
  if (!prev) return null
  return ((cur - prev) / prev) * 100
}
function deltaClass(d: number, goodUp: boolean) {
  if (Math.abs(d) < 0.5) return 'flat'
  return (d > 0) === goodUp ? 'good' : 'bad'
}

const kpis = computed(() => {
  const p = ov.value.period || {}, q = ov.value.period_prev || {}
  const upDown = trafficData.value.map((d: any) => (d.up || 0) + (d.down || 0))
  return [
    {
      key: 'traffic', label: `${days.value}天流量`, value: fmtBytes(p.traffic), color: C.down,
      sub: `累计 ${fmtBytes(ov.value.total_traffic)}`, delta: pctDelta(p.traffic || 0, q.traffic || 0),
      goodUp: true, spark: upDown,
    },
    {
      key: 'online', label: '当前在线', value: String(ov.value.online || 0), color: C.up,
      sub: `总用户 ${ov.value.total_users || 0} · 已开通 ${ov.value.active_users || 0}`,
      delta: null, goodUp: true, onClick: () => { showOnline.value = !showOnline.value },
    },
    {
      key: 'new', label: `${days.value}天新增`, value: String(p.new_users || 0), color: C.up,
      sub: `今日 ${ov.value.new_today || 0}`, delta: pctDelta(p.new_users || 0, q.new_users || 0),
      goodUp: true, spark: regData.value.map((d: any) => d.a || 0),
    },
    {
      key: 'revenue', label: `${days.value}天收入`, value: `${p.revenue || 0} 积分`, color: C.gold,
      sub: `${p.orders || 0} 笔订单 · 在售 ${ov.value.packages_on || 0}`,
      delta: pctDelta(p.revenue || 0, q.revenue || 0), goodUp: true,
      spark: revenueData.value.map((d: any) => d.b || 0),
    },
  ]
})

const distItems = computed(() => [
  { key: 'active', label: '正常', value: dist.value.status_active || 0, color: C.up, filter: { status: 'active' } },
  { key: 'banned', label: '封禁', value: dist.value.status_banned || 0, color: C.red, filter: { status: 'banned' } },
  { key: 'exp7', label: '7天内到期', value: dist.value.expire_7d || 0, color: C.gold, filter: { expiry: 'expiring_7d' } },
  { key: 'exp30', label: '30天内到期', value: dist.value.expire_30d || 0, color: C.gold, filter: {} },
  { key: 'expired', label: '已过期', value: dist.value.expired || 0, color: C.gray, filter: { expiry: 'expired' } },
])

// ---- 迷你趋势线（KPI 卡内联，不值得为它开一个 echarts 实例）----
const Spark = defineComponent({
  props: { data: { type: Array as () => number[], required: true }, color: { type: String, default: '#5e7a99' } },
  setup(props) {
    return () => {
      const d = props.data.filter(n => Number.isFinite(n))
      if (d.length < 2) return null
      const max = Math.max(...d, 1), W = 100, H = 24
      const step = W / (d.length - 1)
      const pts = d.map((v, i) => `${(i * step).toFixed(1)},${(H - (v / max) * H).toFixed(1)}`)
      return h('svg', { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none', class: 'spark' }, [
        h('polyline', { points: `0,${H} ${pts.join(' ')} ${W},${H}`, fill: props.color, opacity: 0.1, stroke: 'none' }),
        h('polyline', { points: pts.join(' '), fill: 'none', stroke: props.color, 'stroke-width': 1.5,
          'stroke-linejoin': 'round', 'vector-effect': 'non-scaling-stroke' }),
      ])
    }
  },
})

// ---- echarts ----
const trafficEl = ref<HTMLElement | null>(null)
const revenueEl = ref<HTMLElement | null>(null)
const regEl = ref<HTMLElement | null>(null)
const pkgPieEl = ref<HTMLElement | null>(null)
const pkgBarEl = ref<HTMLElement | null>(null)
const charts: Record<string, echarts.ECharts> = {}

const baseGrid = { left: 8, right: 12, top: 28, bottom: 4, containLabel: true }
const axisStyle = {
  axisLine: { lineStyle: { color: '#e5e5e5' } },
  axisTick: { show: false },
  axisLabel: { color: '#767676', fontSize: 11 },
}

function draw(key: string, el: HTMLElement | null, option: any) {
  if (!el) return
  // 标签页里的图表在隐藏时宽度为 0，这时 init 会画出一个 0 宽的画布，
  // 切回来也不会自愈——所以宽度为 0 就先不画，等 resize/切页时再来。
  if (!el.clientWidth) return
  if (!charts[key]) charts[key] = echarts.init(el)
  charts[key].setOption(option, true)
}

function trafficOption() {
  const x = trafficData.value.map((d: any) => (d.date || '').slice(5))
  const mk = (name: string, key: string, color: string) => ({
    name, type: 'line', smooth: 0.35, stack: 'total', showSymbol: false,
    lineStyle: { width: 1.5, color },
    areaStyle: {
      color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: color + 'cc' }, { offset: 1, color: color + '14' },
      ]),
    },
    data: trafficData.value.map((d: any) => d[key] || 0),
  })
  return {
    grid: baseGrid,
    tooltip: {
      trigger: 'axis',
      valueFormatter: (v: number) => fmtBytes(v),
      axisPointer: { type: 'line', lineStyle: { color: '#c9c9c9' } },
    },
    legend: { data: ['上行', '下行'], right: 0, top: 0, icon: 'roundRect',
      itemWidth: 9, itemHeight: 9, textStyle: { color: '#595959', fontSize: 11 } },
    xAxis: { type: 'category', boundaryGap: false, data: x, ...axisStyle },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle,
      axisLine: { show: false }, axisLabel: { color: '#767676', fontSize: 11, formatter: (v: number) => fmtBytes(v) } },
    series: [mk('上行', 'up', C.up), mk('下行', 'down', C.down)],
  }
}

function barsOption(data: any[], series: { name: string; key: string; color: string }[], fmt?: (v: number) => string) {
  return {
    grid: { ...baseGrid, top: 24 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, ...(fmt ? { valueFormatter: fmt } : {}) },
    legend: series.length > 1
      ? { data: series.map(s => s.name), right: 0, top: 0, icon: 'roundRect', itemWidth: 9, itemHeight: 9,
          textStyle: { color: '#595959', fontSize: 11 } }
      : { show: false },
    xAxis: { type: 'category', data: data.map((d: any) => (d.date || '').slice(5)), ...axisStyle },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle, axisLine: { show: false } },
    series: series.map(s => ({
      name: s.name, type: 'bar', stack: undefined, barMaxWidth: 14,
      itemStyle: { color: s.color, borderRadius: [3, 3, 0, 0] },
      data: data.map((d: any) => d[s.key] || 0),
    })),
  }
}

function pkgPieOption() {
  const src = packages.value.filter(p => p.revenue > 0)
  if (!src.length) return { title: { text: '暂无成交', left: 'center', top: 'center',
    textStyle: { color: '#767676', fontSize: 13, fontWeight: 400 } }, series: [] }
  return {
    tooltip: { trigger: 'item', formatter: (p: any) => `${p.name}<br/>${p.value} 积分 (${p.percent}%)` },
    legend: { type: 'scroll', orient: 'vertical', right: 0, top: 'center',
      textStyle: { color: '#595959', fontSize: 11 }, itemWidth: 9, itemHeight: 9, icon: 'roundRect' },
    series: [{
      type: 'pie', radius: ['48%', '72%'], center: ['34%', '50%'], avoidLabelOverlap: true,
      itemStyle: { borderColor: '#fff', borderWidth: 2 },
      label: { show: false }, labelLine: { show: false },
      data: src.map((p, i) => ({ name: p.name, value: p.revenue, itemStyle: { color: PIE[i % PIE.length] } })),
    }],
  }
}

function pkgBarOption() {
  const src = [...packages.value].filter(p => p.traffic > 0).sort((a, b) => b.traffic - a.traffic).slice(0, 8).reverse()
  if (!src.length) return { title: { text: '暂无流量', left: 'center', top: 'center',
    textStyle: { color: '#767676', fontSize: 13, fontWeight: 400 } }, series: [] }
  return {
    grid: { left: 8, right: 56, top: 10, bottom: 4, containLabel: true },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, valueFormatter: (v: number) => fmtBytes(v) },
    xAxis: { type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle,
      axisLine: { show: false }, axisLabel: { show: false } },
    yAxis: { type: 'category', data: src.map(p => p.name), ...axisStyle },
    series: [{
      type: 'bar', barMaxWidth: 16, itemStyle: { color: C.down, borderRadius: [0, 4, 4, 0] },
      data: src.map(p => p.traffic),
      label: { show: true, position: 'right', color: '#595959', fontSize: 11,
        formatter: (p: any) => fmtBytes(p.value) },
    }],
  }
}

function renderAll() {
  draw('traffic', trafficEl.value, trafficOption())
  draw('revenue', revenueEl.value, barsOption(revenueData.value,
    [{ name: '发行', key: 'a', color: C.up }, { name: '消费', key: 'b', color: C.gold }]))
  draw('reg', regEl.value, barsOption(regData.value, [{ name: '新增', key: 'a', color: C.down }]))
  draw('pkgPie', pkgPieEl.value, pkgPieOption())
  draw('pkgBar', pkgBarEl.value, pkgBarOption())
}

// 切标签页时被隐藏的图表宽度为 0，回到该页要补画一次。
// 标签页在隐藏时容器宽度为 0，echarts 会画出一个永久 0 宽的画布，所以每次切页
// 都要让当前页重画一次 —— 用量分析在自己的组件里，通过 ref 转达。
watch(tab, () => nextTick(() => { renderAll(); redrawUserChart(); usageReport.value?.refresh?.() }))

function onResize() {
  Object.values(charts).forEach(c => c.resize())
  Object.values(userCharts).forEach(c => c.resize())
}

// ---- 套餐表排序 ----
const pkgCols = [
  { k: 'orders', l: '销量' }, { k: 'revenue', l: '收入' }, { k: 'holders', l: '持有人' },
  { k: 'active_buckets', l: '生效/总份额' }, { k: 'traffic', l: '流量' },
  { k: 'avg', l: '份均流量' }, { k: 'quota', l: '用量占比' }, { k: 'expiring_7d', l: '7天到期' },
]
const pkgSort = ref('revenue')
const pkgDesc = ref(true)
function sortPkg(k: string) {
  if (pkgSort.value === k) pkgDesc.value = !pkgDesc.value
  else { pkgSort.value = k; pkgDesc.value = true }
}
const sortedPackages = computed(() => {
  const k = pkgSort.value
  const val = (p: any) => k === 'avg' ? (p.buckets ? p.traffic / p.buckets : 0)
    : k === 'quota' ? (p.quota ? p.traffic / p.quota : 0) : (p[k] || 0)
  return [...packages.value].sort((a, b) => pkgDesc.value ? val(b) - val(a) : val(a) - val(b))
})
const pkgOptions = computed(() => packages.value.map(p => ({ label: p.name, value: p.package_id })))

// ---- 用户表 ----
const uf = reactive<any>({ q: '', status: null, package_id: null, expiry: null, online: false, sort: 'range_traffic', desc: true })
const users = ref<any[]>([])
const userTotal = ref(0)
const loadingUsers = ref(false)
const page = ref(0)
const pageSize = 20
const expanded = ref<number | null>(null)
const userCharts: Record<number, echarts.ECharts> = {}
const userChartEls: Record<number, HTMLElement> = {}

const filterActive = computed(() => !!(uf.q || uf.status || uf.package_id || uf.expiry || uf.online))

function sortUser(k: string) {
  if (uf.sort === k) uf.desc = !uf.desc
  else { uf.sort = k; uf.desc = true }
  loadUsers()
}
function clearFilters() {
  uf.q = ''; uf.status = null; uf.package_id = null; uf.expiry = null; uf.online = false
  loadUsers()
}
function goPage(p: number) { page.value = p; loadUsers(false) }

// 跳到用户分析并套用筛选：让「7天内到期 12 人」这种数字可以点进去看到底是谁。
function drillUsers(f: Record<string, any>) {
  clearFiltersSilently()
  Object.assign(uf, f)
  tab.value = 'user'
  loadUsers()
}
function clearFiltersSilently() {
  uf.q = ''; uf.status = null; uf.package_id = null; uf.expiry = null; uf.online = false
}

let debounceTimer: any = null
function debouncedUsers() {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => loadUsers(), 300)
}

async function loadUsers(resetPage = true) {
  if (resetPage) page.value = 0
  loadingUsers.value = true
  const p = new URLSearchParams({ range: range.value, sort: uf.sort, desc: uf.desc ? '1' : '0',
    limit: String(pageSize), offset: String(page.value * pageSize) })
  if (uf.q) p.set('q', uf.q)
  if (uf.status) p.set('status', uf.status)
  if (uf.package_id) p.set('package_id', String(uf.package_id))
  if (uf.expiry) p.set('expiry', uf.expiry)
  if (uf.online) p.set('online', '1')
  try {
    const d = await apiGet<any>('/api/admin/stats/users?' + p.toString())
    users.value = d?.rows || []
    userTotal.value = d?.total || 0
    // 展开的行可能已经不在当前结果里了，收起来免得挂着一个空图表。
    if (expanded.value && !users.value.some(u => u.id === expanded.value)) expanded.value = null
  } catch (e: any) { message.error(e.message) } finally { loadingUsers.value = false }
}

function isOnline(u: any) { return !!u.online }
function expiryClass(ts: number) {
  if (!ts) return 'muted'
  const left = ts - Date.now() / 1000
  if (left <= 0) return 'bad'
  if (left < 7 * 86400) return 'warn'
  return ''
}

function setUserChartEl(id: number, el: any) {
  if (el) userChartEls[id] = el
  else delete userChartEls[id]
}

async function toggleUser(u: any) {
  if (expanded.value === u.id) { expanded.value = null; return }
  expanded.value = u.id
  await nextTick()
  try {
    const rows = await apiList(`/api/admin/stats/user/${u.id}/traffic?range=${range.value}`)
    const el = userChartEls[u.id]
    if (!el) return
    if (!userCharts[u.id]) userCharts[u.id] = echarts.init(el)
    userCharts[u.id].setOption(trafficOptionFor(rows as any[]), true)
  } catch (e: any) { message.error(e.message) }
}

function trafficOptionFor(rows: any[]) {
  return {
    grid: { left: 8, right: 12, top: 22, bottom: 4, containLabel: true },
    tooltip: { trigger: 'axis', valueFormatter: (v: number) => fmtBytes(v) },
    legend: { data: ['上行', '下行'], right: 0, top: 0, icon: 'roundRect', itemWidth: 9, itemHeight: 9,
      textStyle: { color: '#595959', fontSize: 11 } },
    xAxis: { type: 'category', boundaryGap: false, data: rows.map(d => (d.date || '').slice(5)), ...axisStyle },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle,
      axisLine: { show: false }, axisLabel: { color: '#767676', fontSize: 11, formatter: (v: number) => fmtBytes(v) } },
    series: [
      { name: '上行', type: 'line', smooth: 0.35, showSymbol: false, lineStyle: { width: 1.5, color: C.up },
        areaStyle: { color: C.up + '22' }, data: rows.map(d => d.up || 0) },
      { name: '下行', type: 'line', smooth: 0.35, showSymbol: false, lineStyle: { width: 1.5, color: C.down },
        areaStyle: { color: C.down + '22' }, data: rows.map(d => d.down || 0) },
    ],
  }
}

function redrawUserChart() {
  if (expanded.value && userCharts[expanded.value]) userCharts[expanded.value].resize()
}

// ---- 加载 ----
async function reload() {
  await Promise.all([
    (async () => {
      try {
        const d = await apiGet<any>(`/api/admin/stats/overview?range=${range.value}`)
        ov.value = d || {}
        onlineUsers.value = d?.online_users || []
      } catch {}
    })(),
    (async () => { try { trafficData.value = await apiList(`/api/admin/stats/traffic?range=${range.value}`) } catch {} })(),
    (async () => {
      try {
        // 后端按同一个 range 补齐了空缺的日期，这里直接用：不能在前端 slice，
        // 稀疏数组的「最后 14 条」是「最近 14 个有数据的天」，不是近 14 天。
        const d = await apiGet<any>(`/api/admin/stats/distribution?range=${range.value}`)
        dist.value = d?.distribution || {}
        revenueData.value = d?.revenue || []
        regData.value = d?.registration || []
      } catch {}
    })(),
    (async () => { try { packages.value = await apiList('/api/admin/stats/packages') } catch {} })(),
    loadUsers(),
  ])
  await nextTick()
  renderAll()
}

onMounted(async () => {
  await reload()
  window.addEventListener('resize', onResize)
})
onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  clearTimeout(debounceTimer)
  Object.values(charts).forEach(c => c.dispose())
  Object.values(userCharts).forEach(c => c.dispose())
})
</script>

<style scoped>
.ov-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
.page-sub { color: var(--text-2); margin: 0; font-size: 13px; }

/* KPI */
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 12px; margin-bottom: 16px; }
.kpi {
  position: relative; overflow: hidden;
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 14px 16px 10px; transition: box-shadow .15s, border-color .15s, transform .15s;
}
.kpi.clickable { cursor: pointer; }
.kpi:hover { box-shadow: var(--shadow); border-color: #d5d5d5; }
.kpi-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.kpi-label { font-size: 12.5px; color: var(--text-2); font-weight: 550; }
.kpi-delta { font-size: 11px; font-weight: 650; padding: 1px 6px; border-radius: 20px; background: var(--bg-soft); color: var(--text-3); }
.kpi-delta.good { color: #4d7256; background: #eef4ef; }
.kpi-delta.bad { color: #a8564b; background: #f9eeec; }
.kpi-value { font-size: 26px; font-weight: 720; letter-spacing: -0.02em; margin-top: 6px; line-height: 1.15; }
.kpi-sub { font-size: 11.5px; color: var(--text-3); margin-top: 2px; }
.kpi-spark { height: 24px; margin: 6px -16px -10px; }
:deep(.spark) { width: 100%; height: 24px; display: block; }

/* 区块 */
.sec { margin-bottom: 14px; border-radius: var(--r-sm); }
.sec-title { font-weight: 650; font-size: 14px; }
.sec-note { font-size: 11.5px; color: var(--text-3); margin-left: 10px; font-weight: 400; }
.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.chart { width: 100%; }
.empty { text-align: center; color: var(--text-3); padding: 22px; font-size: 13px; }
.hint { font-size: 11.5px; color: var(--text-3); margin: 10px 0 0; }
.muted { color: var(--text-3); font-size: 12px; }
.ov-tabs :deep(.n-tabs-nav) { margin-bottom: 12px; }

/* 在线 */
.online-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 6px 14px; }
.online-item { display: flex; align-items: center; gap: 6px; font-size: 13px; padding: 3px 0; }
.on-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.on-time { font-size: 11px; color: var(--text-3); }
.dot { width: 7px; height: 7px; border-radius: 50%; background: #6f8f76; flex-shrink: 0; display: inline-block; }
.dot.off { background: #d5d5d5; }

/* 分布 */
.dist-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(110px, 1fr)); gap: 10px; }
.dist-item {
  font: inherit; cursor: pointer; border: 1px solid var(--border); background: var(--bg-soft);
  padding: 12px 10px; border-radius: var(--r-sm); text-align: center;
  display: flex; flex-direction: column; gap: 2px; transition: border-color .15s, background .15s;
}
.dist-item:hover { border-color: #cfcfcf; background: #fff; }
.dist-val { font-size: 22px; font-weight: 720; letter-spacing: -0.01em; }
.dist-label { font-size: 11.5px; color: var(--text-3); }

/* 表格 */
.tbl-wrap { overflow-x: auto; }
.tbl { width: 100%; border-collapse: collapse; font-size: 13px; white-space: nowrap; }
.tbl th, .tbl td { padding: 8px 10px; text-align: right; border-bottom: 1px solid var(--border); }
.tbl th { font-size: 11.5px; font-weight: 600; color: var(--text-3); background: var(--bg-soft); position: sticky; top: 0; }
.tbl th:first-child { border-radius: 6px 0 0 0; }
.tbl th:last-child { border-radius: 0 6px 0 0; }
.tbl th.l, .tbl td.l { text-align: left; }
.tbl th.sortable { cursor: pointer; user-select: none; }
.tbl th.sortable:hover { color: var(--text); }
.tbl th.on { color: var(--text); }
.tbl tbody tr:hover { background: var(--bg-soft); }
.tbl tbody tr.open { background: var(--bg-soft); }
.tbl tbody tr.drill:hover { background: var(--bg-soft); }
.tbl td.warn, .tbl .warn { color: #a17a2e; }
.tbl td.bad, .tbl .bad { color: #a8564b; }
.pkg-name, .u-name { font-weight: 600; color: var(--text); }
.mini-bar { display: inline-block; width: 54px; height: 5px; border-radius: 3px; background: #ececec; overflow: hidden; vertical-align: middle; margin-right: 6px; }
.mini-bar i { display: block; height: 100%; background: #5e7a99; border-radius: 3px; }
.mini-bar i.hot { background: #c2685c; }

.filters { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.filters .spacer { flex: 1; }
.pager { display: flex; align-items: center; justify-content: center; gap: 12px; margin-top: 12px; }

@media (max-width: 900px) {
  .two-col { grid-template-columns: 1fr; }
}
</style>
