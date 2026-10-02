<template>
  <div class="usage">
    <AdminMachineUsage class="sec" />
    <n-card title="套餐 · 当前可用用户" size="small" class="sec">
      <n-select v-model:value="audiencePackage" :options="pkgOptions.filter(p => p.value !== -1)" placeholder="选择套餐，查看所有当前生效用户（含零用量）" clearable filterable />
      <AdminAudience v-if="audiencePackage !== null" scope="package" :id="audiencePackage" />
      <p class="scope">下方历史用量报表可按套餐筛选并查看用户排名；当前生效名单与历史实际消耗分别统计，过期套餐的历史消耗仍保留。</p>
    </n-card>
    <!-- 控制条：时间口径 + 用户选择。两者共同决定下面所有数字，所以放在最上面
         而不是各图表各自带筛选，避免几块数据各说各的口径。 -->
    <n-card size="small" class="sec">
      <div class="ctl">
        <div class="ctl-group">
          <span class="ctl-label">时间</span>
          <n-radio-group v-model:value="preset" size="small" @update:value="onPreset">
            <n-radio-button v-for="p in presets" :key="p.v" :value="p.v">{{ p.l }}</n-radio-button>
          </n-radio-group>
          <n-date-picker
            v-if="preset === 'custom'"
            v-model:value="customRange"
            type="daterange"
            size="small"
            clearable
            :is-date-disabled="disableFuture"
            style="width:250px;"
            @update:value="load"
          />
        </div>

        <div class="ctl-group grow">
          <span class="ctl-label">套餐</span>
          <n-select
            v-model:value="selectedPkgs"
            multiple
            filterable
            clearable
            size="small"
            placeholder="全部套餐"
            :options="pkgOptions"
            :max-tag-count="3"
            class="user-select"
            @update:value="load"
          />
        </div>

        <div class="ctl-group grow">
          <span class="ctl-label">用户</span>
          <n-select
            v-model:value="selected"
            multiple
            filterable
            clearable
            size="small"
            placeholder="全部用户（可搜索选择，最多 100 个）"
            :options="userOptions"
            :loading="loadingUsers"
            :max-tag-count="4"
            class="user-select"
            @update:value="load"
          />
          <n-button v-if="selected.length || selectedPkgs.length" size="small" quaternary @click="clearFilters">
            清空筛选
          </n-button>
        </div>
      </div>

      <!-- 数据口径说明。这不是装饰：累计模式用的是账户计数器（一直准），
           区间模式用的是每日汇总表（只从开始记录那天起）。不写清楚，
           管理员会把「区间为 0」读成「没用流量」。 -->
      <p class="scope">
        <template v-if="mode === 'lifetime'">
          <b>累计口径</b>：账户开通至今的全部流量，含本功能上线前的历史。
        </template>
        <template v-else>
          <b>区间口径</b>：{{ rangeLabel }}。
          <template v-if="coverage.first">
            明细数据自 <b>{{ coverage.first }}</b> 起记录<template v-if="hasUnattributed">，更早的流量无法归属到具体套餐，已单列为「{{ UNATTRIBUTED }}」</template>。
          </template>
          <template v-else>暂无区间明细数据，需等待首次流量统计。</template>
        </template>
      </p>
    </n-card>

    <!-- KPI -->
    <div class="kpi-row">
      <div v-for="k in kpis" :key="k.key" class="kpi">
        <div class="kpi-label">{{ k.label }}</div>
        <div class="kpi-value" :style="{ color: k.color }">{{ k.value }}</div>
        <div class="kpi-sub">{{ k.sub }}</div>
      </div>
    </div>

    <n-spin :show="loading">
      <!-- 趋势：选了用户就按用户分层堆叠，没选就是全站合计 -->
      <n-card size="small" class="sec">
        <template #header>
          <span class="sec-title">流量趋势</span>
          <span class="sec-note">
            {{ selected.length ? `${selected.length} 位用户分层对比` : '全站合计' }}
            <template v-if="mode === 'lifetime'"> · 曲线仅覆盖已记录区间</template>
          </span>
        </template>
        <div v-if="!hasSeries" class="empty">该区间没有流量记录</div>
        <div v-show="hasSeries" ref="trendEl" class="chart" style="height:300px;" />
      </n-card>

      <div class="two-col">
        <n-card size="small" class="sec">
          <template #header>
            <span class="sec-title">套餐分布</span>
            <span class="sec-note">按流量占比 · 点击扇区筛选</span>
          </template>
          <div v-if="!byPackage.length" class="empty">暂无套餐用量</div>
          <div v-show="byPackage.length" ref="pkgEl" class="chart" style="height:280px;" />
        </n-card>

        <n-card size="small" class="sec">
          <template #header>
            <span class="sec-title">用户排行</span>
            <span class="sec-note">前 {{ Math.min(byUser.length, 10) }} 名 · 点击柱条筛选</span>
          </template>
          <div v-if="!byUser.length" class="empty">暂无用户用量</div>
          <div v-show="byUser.length" ref="rankEl" class="chart" style="height:280px;" />
        </n-card>
      </div>

      <!-- 同一份数据提供两个口径：用户汇总用于判断账号整体流量需求，套餐明细用于继续追溯消耗来源。 -->
      <n-card size="small" class="sec">
        <template #header>
          <span class="sec-title">{{ tableMode === 'user' ? '用户流量汇总' : '用户 · 套餐明细' }}</span>
          <span class="sec-note">
            {{ tableMode === 'user' ? `合并同一用户的全部套餐 · 共 ${userSummary.length} 位用户` : `共 ${detail.length} 行` }}
          </span>
          <n-radio-group v-model:value="tableMode" size="small" class="table-mode">
            <n-radio-button value="user">按用户汇总</n-radio-button>
            <n-radio-button value="package">按套餐明细</n-radio-button>
          </n-radio-group>
          <n-button size="tiny" quaternary :disabled="!tableRows.length" @click="exportCSV">
            导出 CSV
          </n-button>
        </template>
        <div v-if="!tableRows.length" class="empty">没有可展示的统计</div>
        <div v-else class="tbl-wrap">
          <table class="tbl">
            <thead>
              <tr>
                <th class="l">用户</th>
                <th class="l">{{ tableMode === 'user' ? '合并套餐' : '套餐' }}</th>
                <th v-for="c in cols" :key="c.k" class="sortable" :class="{ on: sortKey === c.k }" @click="sortBy(c.k)">
                  {{ c.l }}<span v-if="sortKey === c.k">{{ sortDesc ? ' ↓' : ' ↑' }}</span>
                </th>
                <th class="l">相对消耗</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in sortedTableRows" :key="tableMode === 'user' ? d.user_id : `${d.user_id}:${d.package_id}`">
                <td class="l"><span class="u-name">{{ d.username || '—' }}</span></td>
                <td v-if="tableMode === 'user'" class="l merged-packages">
                  <b>{{ d.package_count }} 个套餐</b>
                  <span :title="d.package_names.join('、')">{{ d.package_names.join('、') || '暂无可归属套餐' }}</span>
                </td>
                <td v-else class="l">
                  <span class="pkg" :class="{ synthetic: d.package_id <= 0 }">{{ d.package_name }}</span>
                </td>
                <td>{{ fmtBytes(d.up) }}</td>
                <td>{{ fmtBytes(d.down) }}</td>
                <td><b>{{ fmtBytes(d.up + d.down) }}</b></td>
                <td class="l">
                  <div class="ratio">
                    <div class="ratio-bar"><i :style="{ width: pct(d) + '%' }" /></div>
                    <span class="ratio-num">{{ pct(d).toFixed(1) }}%</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </n-card>
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import AdminMachineUsage from './AdminMachineUsage.vue'
import AdminAudience from './AdminAudience.vue'
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
const audiencePackage = ref<number | null>(null)
import { NCard, NRadioGroup, NRadioButton, NSelect, NButton, NSpin, NDatePicker, useMessage } from 'naive-ui'
import * as echarts from 'echarts'
import { apiGet, apiList } from '@/api'
import { fmtBytes } from '@/utils/format'

const message = useMessage()

// 与 AdminOverview 同一套暖中性色板，跨页视觉一致。
const C = { up: '#6f8f76', down: '#5e7a99', gold: '#bf9540', red: '#c2685c', gray: '#9aa0a6' }
const PALETTE = ['#5e7a99', '#6f8f76', '#bf9540', '#c2685c', '#8d7fa8', '#7f9ea8', '#a89a7f', '#9aa0a6',
                 '#7b93b3', '#85a58c']
const UNATTRIBUTED = '未记录套餐（升级前）'

const presets = [
  { v: 'total', l: '累计' },
  { v: '7d', l: '7天' },
  { v: '30d', l: '30天' },
  { v: '90d', l: '90天' },
  { v: 'custom', l: '自定义' },
]

const preset = ref('30d')
const customRange = ref<[number, number] | null>(null)
const selected = ref<number[]>([])
const selectedPkgs = ref<number[]>([])
const loading = ref(false)
const loadingUsers = ref(false)

const mode = ref('window')
const coverage = ref<{ first: string; last: string }>({ first: '', last: '' })
const series = ref<any[]>([])
const totalSeries = ref<any[]>([])
const byUser = ref<any[]>([])
const byPackage = ref<any[]>([])
const candidates = ref<any[]>([])
const pkgCandidates = ref<any[]>([])

const userOptions = computed(() =>
  candidates.value.map(c => ({ label: `${c.username}（${fmtBytes(c.traffic)}）`, value: c.id })))
const pkgOptions = computed(() =>
  pkgCandidates.value.map(c => ({ label: `${c.name}（${fmtBytes(c.traffic)}）`, value: c.id })))
// 套餐名 -> id，供环图点击回填筛选（图上只有名字，没有 id）
const pkgIdByName = computed(() => {
  const m = new Map<string, number>()
  pkgCandidates.value.forEach(c => m.set(c.name, c.id))
  return m
})

// 未来日期没有数据，选了只会得到空图 —— 直接禁掉比让人选完再解释更好。
function disableFuture(ts: number) { return ts > Date.now() }

const rangeLabel = computed(() => {
  if (preset.value === 'custom' && customRange.value) {
    return `${toDay(customRange.value[0])} 至 ${toDay(customRange.value[1])}`
  }
  const l = presets.find(p => p.v === preset.value)?.l
  return l ? `最近 ${l}` : ''
})

const hasUnattributed = computed(() => byPackage.value.some(p => p.package_id < 0))
const hasSeries = computed(() => totalSeries.value.length > 0)

const sumUp = computed(() => byUser.value.reduce((s, d) => s + (d.up || 0), 0))
const sumDown = computed(() => byUser.value.reduce((s, d) => s + (d.down || 0), 0))
const grandTotal = computed(() => sumUp.value + sumDown.value)

const kpis = computed(() => {
  const days = totalSeries.value.length
  const avg = days ? grandTotal.value / days : 0
  const peak = totalSeries.value.reduce(
    (m: any, d: any) => ((d.up + d.down) > (m.v || 0) ? { v: d.up + d.down, date: d.date } : m), {} as any)
  return [
    { key: 'total', label: '总流量', value: fmtBytes(grandTotal.value), color: C.down,
      sub: `${byUser.value.length} 位用户产生` },
    { key: 'updown', label: '上行 / 下行', value: `${fmtBytes(sumUp.value)} / ${fmtBytes(sumDown.value)}`,
      color: C.up, sub: grandTotal.value ? `下行占 ${(sumDown.value / grandTotal.value * 100).toFixed(0)}%` : '—' },
    { key: 'avg', label: '日均', value: fmtBytes(avg), color: C.gold,
      sub: days ? `覆盖 ${days} 个有流量的日子` : '暂无每日数据' },
    { key: 'peak', label: '单日峰值', value: peak.v ? fmtBytes(peak.v) : '—', color: C.red,
      sub: peak.date || '暂无每日数据' },
  ]
})

// ---- 明细表 ----
const cols = [{ k: 'up', l: '上行' }, { k: 'down', l: '下行' }, { k: 'total', l: '合计' }]
const sortKey = ref('total')
const sortDesc = ref(true)
const tableMode = ref<'user' | 'package'>('user')
const detail = computed(() => byPackage.value)
const userSummary = computed(() => {
  const packages = new Map<number, Map<number, string>>()
  byPackage.value.forEach((d: any) => {
    if (!packages.has(d.user_id)) packages.set(d.user_id, new Map())
    packages.get(d.user_id)!.set(d.package_id, d.package_name)
  })
  return byUser.value.map((d: any) => {
    const userPackages = packages.get(d.user_id)
    return {
      ...d,
      package_count: userPackages?.size || 0,
      package_names: userPackages ? [...userPackages.values()] : [],
    }
  })
})
const tableRows = computed(() => tableMode.value === 'user' ? userSummary.value : detail.value)
const maxTableRow = computed(() => Math.max(...tableRows.value.map(d => d.up + d.down), 1))

const sortedTableRows = computed(() => {
  const rows = [...tableRows.value]
  const get = (d: any) => sortKey.value === 'total' ? d.up + d.down : d[sortKey.value] || 0
  rows.sort((a, b) => sortDesc.value ? get(b) - get(a) : get(a) - get(b))
  return rows
})

function sortBy(k: string) {
  if (sortKey.value === k) sortDesc.value = !sortDesc.value
  else { sortKey.value = k; sortDesc.value = true }
}

// 占比以「本表最大行」为基准而非总量：几十行时按总量算出来的条全是细线，看不出差异。
function pct(d: any) { return (d.up + d.down) / maxTableRow.value * 100 }

function exportCSV() {
  const esc = (s: any) => `"${String(s ?? '').replace(/"/g, '""')}"`
  const isUser = tableMode.value === 'user'
  const head = isUser
    ? ['用户', '套餐数', '合并套餐', '上行(字节)', '下行(字节)', '合计(字节)']
    : ['用户', '套餐', '上行(字节)', '下行(字节)', '合计(字节)']
  const body = sortedTableRows.value.map(d => {
    const row = isUser
      ? [d.username, d.package_count, d.package_names.join('、'), d.up, d.down, d.up + d.down]
      : [d.username, d.package_name, d.up, d.down, d.up + d.down]
    return row.map(esc).join(',')
  })
  // BOM：没有它 Excel 会把中文列名读成乱码。
  const blob = new Blob(['﻿' + [head.map(esc).join(','), ...body].join('\r\n')],
    { type: 'text/csv;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `${isUser ? '用户流量汇总' : '用户套餐明细'}_${preset.value}_${toDay(Date.now())}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
  message.success('已导出')
}

// ---- 图表 ----
const trendEl = ref<HTMLElement | null>(null)
const pkgEl = ref<HTMLElement | null>(null)
const rankEl = ref<HTMLElement | null>(null)
const charts: Record<string, echarts.ECharts> = {}

const axisStyle = {
  axisLine: { lineStyle: { color: '#e5e5e5' } },
  axisTick: { show: false },
  axisLabel: { color: '#767676', fontSize: 11 },
}
const byteAxis = {
  type: 'value', splitLine: { lineStyle: { color: '#f1f1f1' } }, ...axisStyle,
  axisLine: { show: false },
  axisLabel: { color: '#767676', fontSize: 11, formatter: (v: number) => fmtBytes(v) },
}

function draw(key: string, el: HTMLElement | null, option: any, onClick?: (p: any) => void) {
  if (!el || !el.clientWidth) return // 隐藏容器宽度为 0，此时 init 会画出永久 0 宽画布
  if (!charts[key]) {
    charts[key] = echarts.init(el)
    // 只在实例创建时绑一次。setOption 不会清掉监听器，每次重画都绑会导致
    // 点一下触发 N 次 load()。
    if (onClick) charts[key].on('click', onClick)
  }
  charts[key].setOption(option, true)
}

// 所有出现过的日期的并集，作为统一 x 轴 —— 各用户的稀疏序列必须对齐到同一根轴上，
// 否则同一个 x 位置在两条线里代表不同的日子。
const axisDays = computed(() => {
  const s = new Set<string>()
  totalSeries.value.forEach((d: any) => s.add(d.date))
  series.value.forEach((u: any) => u.days.forEach((d: any) => s.add(d.date)))
  return [...s].sort()
})

function trendOption() {
  const x = axisDays.value
  const mkStack = (name: string, days: any[], color: string) => {
    const m = new Map(days.map((d: any) => [d.date, d.up + d.down]))
    return {
      name, type: 'line', smooth: 0.35, stack: 'u', showSymbol: false,
      lineStyle: { width: 1.2, color },
      areaStyle: {
        color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: color + 'cc' }, { offset: 1, color: color + '18' },
        ]),
      },
      data: x.map(d => m.get(d) ?? 0),
    }
  }

  let s: any[]
  if (selected.value.length && series.value.length) {
    s = series.value.map((u: any, i: number) => mkStack(u.username || `#${u.user_id}`, u.days, PALETTE[i % PALETTE.length]))
  } else {
    const up = new Map(totalSeries.value.map((d: any) => [d.date, d.up]))
    const down = new Map(totalSeries.value.map((d: any) => [d.date, d.down]))
    s = [
      { name: '上行', type: 'line', smooth: 0.35, stack: 't', showSymbol: false,
        lineStyle: { width: 1.5, color: C.up },
        areaStyle: { color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: C.up + 'cc' }, { offset: 1, color: C.up + '14' }]) },
        data: x.map(d => up.get(d) ?? 0) },
      { name: '下行', type: 'line', smooth: 0.35, stack: 't', showSymbol: false,
        lineStyle: { width: 1.5, color: C.down },
        areaStyle: { color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: C.down + 'cc' }, { offset: 1, color: C.down + '14' }]) },
        data: x.map(d => down.get(d) ?? 0) },
    ]
  }

  return {
    grid: { left: 8, right: 12, top: 34, bottom: 4, containLabel: true },
    tooltip: {
      trigger: 'axis',
      valueFormatter: (v: number) => fmtBytes(v),
      // 多用户堆叠时按值降序，最大的贡献者排最前，省得在十条里找。
      order: 'valueDesc',
    },
    legend: { top: 0, icon: 'roundRect', itemWidth: 10, itemHeight: 10,
      textStyle: { color: '#767676', fontSize: 11 }, type: 'scroll' },
    xAxis: { type: 'category', boundaryGap: false, data: x.map(d => d.slice(5)), ...axisStyle },
    yAxis: byteAxis,
    series: s,
  }
}

function pkgOption() {
  // 同名套餐跨用户合并 —— 环图问的是「哪个套餐吃掉了流量」，不是「谁的哪个套餐」。
  const agg = new Map<string, number>()
  byPackage.value.forEach((p: any) => {
    agg.set(p.package_name, (agg.get(p.package_name) || 0) + p.up + p.down)
  })
  const data = [...agg.entries()]
    .map(([name, value]) => ({ name, value }))
    .sort((a, b) => b.value - a.value)
  return {
    tooltip: { trigger: 'item', valueFormatter: (v: number) => fmtBytes(v) },
    legend: { type: 'scroll', orient: 'vertical', right: 0, top: 'center',
      textStyle: { color: '#767676', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    series: [{
      type: 'pie', radius: ['52%', '76%'], center: ['36%', '50%'], cursor: 'pointer',
      avoidLabelOverlap: true, itemStyle: { borderColor: '#fff', borderWidth: 2 },
      label: { show: false }, labelLine: { show: false },
      data: data.map((d, i) => {
        const id = pkgIdByName.value.get(d.name)
        const dim = selectedPkgs.value.length && id !== undefined && !selectedPkgs.value.includes(id)
        return { ...d, itemStyle: { color: PALETTE[i % PALETTE.length], opacity: dim ? 0.25 : 1 } }
      }),
    }],
  }
}

function rankOption() {
  const rows = [...byUser.value].sort((a, b) => (b.up + b.down) - (a.up + a.down)).slice(0, 10).reverse()
  return {
    grid: { left: 8, right: 24, top: 12, bottom: 4, containLabel: true },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, valueFormatter: (v: number) => fmtBytes(v) },
    xAxis: byteAxis,
    yAxis: { type: 'category', data: rows.map(r => r.username || `#${r.user_id}`), ...axisStyle },
    series: [{
      type: 'bar', barMaxWidth: 16, cursor: 'pointer',
      itemStyle: { borderRadius: [0, 3, 3, 0], color: C.down },
      data: rows.map(r => r.up + r.down),
      label: { show: true, position: 'right', color: '#767676', fontSize: 11,
        formatter: (p: any) => fmtBytes(p.value) },
    }],
  }
}

function renderAll() {
  draw('trend', trendEl.value, trendOption())
  draw('pkg', pkgEl.value, pkgOption(), onPkgClick)
  draw('rank', rankEl.value, rankOption(), onRankClick)
}

// 环图按套餐名聚合（同名跨用户合并），所以点击只能拿到名字，需要回查 id。
function onPkgClick(p: any) {
  const id = pkgIdByName.value.get(p?.name)
  if (id === undefined) return
  toggleIn(selectedPkgs.value, id)
  load()
}

function onRankClick(p: any) {
  const row = [...byUser.value]
    .sort((a, b) => (b.up + b.down) - (a.up + a.down))
    .slice(0, 10).reverse()[p?.dataIndex]
  if (!row) return
  toggleIn(selected.value, row.user_id)
  load()
}

let resizeTimer: any
function onResize() {
  clearTimeout(resizeTimer)
  resizeTimer = setTimeout(() => {
    Object.values(charts).forEach(c => c.resize())
    renderAll() // 之前因宽度为 0 跳过的图，在这里补画
  }, 120)
}

// ---- 加载 ----
function toDay(ts: number) {
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function onPreset(v: string) {
  // 切到自定义时给一个合理的默认区间，省得面对空 picker 还要自己想选哪天。
  if (v === 'custom' && !customRange.value) {
    customRange.value = [Date.now() - 29 * 86400_000, Date.now()]
  }
  load()
}

function clearFilters() {
  selected.value = []
  selectedPkgs.value = []
  load()
}

// 图表点击 = 筛选。再点一次同一项则取消，否则没有回到「全部」的路径。
function toggleIn(list: number[], id: number) {
  const i = list.indexOf(id)
  if (i >= 0) list.splice(i, 1)
  else list.push(id)
}

function buildQuery() {
  const q = new URLSearchParams()
  q.set('preset', preset.value)
  if (preset.value === 'custom' && customRange.value) {
    q.set('from', toDay(customRange.value[0]))
    q.set('to', toDay(customRange.value[1]))
  }
  if (selected.value.length) q.set('users', selected.value.join(','))
  if (selectedPkgs.value.length) q.set('packages', selectedPkgs.value.join(','))
  return q.toString()
}

async function load() {
  // 自定义但还没选日期：不要发请求，否则后端会回落到 30 天，
  // 界面显示「自定义」而数据是 30 天的，口径对不上。
  if (preset.value === 'custom' && !customRange.value) return
  loading.value = true
  try {
    const d = await apiGet<any>(`/api/admin/stats/usage?${buildQuery()}`)
    mode.value = d?.mode || 'window'
    coverage.value = d?.coverage || { first: '', last: '' }
    series.value = d?.series || []
    totalSeries.value = d?.total_series || []
    byUser.value = d?.by_user || []
    byPackage.value = d?.by_package || []
  } catch (e: any) {
    message.error(e?.message || '读取用量统计失败')
  } finally {
    loading.value = false
  }
  await nextTick()
  renderAll()
}

async function loadUsers() {
  loadingUsers.value = true
  try {
    candidates.value = await apiList('/api/admin/stats/usage/users?limit=300')
  } catch {} finally {
    loadingUsers.value = false
  }
}

async function loadPkgs() {
  try { pkgCandidates.value = await apiList('/api/admin/stats/usage/packages') } catch {}
}

// 父组件切到本标签页时容器才有宽度，此时补画。
defineExpose({ refresh: () => { renderAll() } })

watch(() => [byUser.value, byPackage.value], () => nextTick(renderAll), { deep: false })

onMounted(async () => {
  await Promise.all([loadUsers(), loadPkgs(), load()])
  window.addEventListener('resize', onResize)
})
onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  clearTimeout(resizeTimer)
  Object.values(charts).forEach(c => c.dispose())
})
</script>

<style scoped>
.sec { margin-bottom: 14px; border-radius: var(--r-sm); }
.sec-title { font-weight: 650; font-size: 13.5px; }
.sec-note { color: var(--text-3); font-size: 11.5px; margin-left: 8px; }
.chart { width: 100%; }
.empty { text-align: center; color: var(--text-3); padding: 40px 0; font-size: 12.5px; }

/* 控制条 */
.ctl { display: flex; flex-wrap: wrap; gap: 12px 20px; align-items: center; }
.ctl-group { display: flex; align-items: center; gap: 8px; }
.ctl-group.grow { flex: 1; min-width: 280px; }
.ctl-label { font-size: 12px; color: var(--text-2); font-weight: 550; white-space: nowrap; }
.user-select { flex: 1; min-width: 220px; }
.scope {
  margin: 10px 0 0; padding-top: 10px; border-top: 1px solid var(--border);
  font-size: 11.5px; color: var(--text-3); line-height: 1.6;
}
.scope b { color: var(--text-2); font-weight: 650; }

/* KPI */
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 12px; margin-bottom: 14px; }
.kpi {
  background: var(--card); border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 13px 15px; transition: box-shadow .15s, border-color .15s;
}
.kpi:hover { box-shadow: var(--shadow); border-color: #d5d5d5; }
.kpi-label { font-size: 12px; color: var(--text-2); font-weight: 550; }
.kpi-value { font-size: 21px; font-weight: 720; letter-spacing: -0.02em; margin-top: 5px; line-height: 1.2; }
.kpi-sub { font-size: 11px; color: var(--text-3); margin-top: 3px; }

/* minmax(0, 1fr), not 1fr: a bare `1fr` is minmax(auto, 1fr), and `auto` floors
   the track at the item's min-content width. An echarts canvas keeps its last
   pixel width until resize() is called, so on a narrowing viewport the stale
   canvas holds the track open, resize() then measures that same held-open
   container, and the column never shrinks — the chart stays desktop-wide inside
   a phone-width card. Verified at 375px: without this the cards render 457px in
   a 351px container. */
.two-col { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 14px; }
@media (max-width: 900px) { .two-col { grid-template-columns: minmax(0, 1fr); } }
/* Same reasoning one level down: the chart box must be allowed to shrink rather
   than be sized by the canvas it contains. */
.chart { min-width: 0; }

/* 表格 */
.tbl-wrap { overflow-x: auto; }
.tbl { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.tbl th, .tbl td { padding: 8px 10px; text-align: right; white-space: nowrap; }
.tbl th.l, .tbl td.l { text-align: left; }
.tbl thead th { color: var(--text-3); font-weight: 600; font-size: 11.5px; border-bottom: 1px solid var(--border); }
.tbl tbody tr { border-bottom: 1px solid var(--border-soft, #f2f2f2); }
.tbl tbody tr:hover { background: var(--bg-soft); }
.tbl th.sortable { cursor: pointer; user-select: none; }
.tbl th.sortable:hover, .tbl th.on { color: var(--text-1); }
.u-name { font-weight: 600; }
.pkg { color: var(--text-2); }
.pkg.synthetic { color: var(--text-3); font-style: italic; }
.table-mode { margin-left: auto; }
.merged-packages { max-width: 360px; }
.merged-packages b { margin-right: 8px; font-weight: 650; color: var(--text-2); }
.merged-packages span {
  display: inline-block; max-width: 260px; overflow: hidden; text-overflow: ellipsis;
  vertical-align: bottom; color: var(--text-3);
}

.ratio { display: flex; align-items: center; gap: 8px; min-width: 120px; }
.ratio-bar { flex: 1; height: 5px; background: var(--bg-soft); border-radius: 3px; overflow: hidden; }
.ratio-bar i { display: block; height: 100%; background: #5e7a99; border-radius: 3px; }
.ratio-num { color: var(--text-3); font-size: 11px; min-width: 38px; text-align: right; }
@media (max-width: 700px) {
  .table-mode { order: 3; margin-left: 0; }
  .merged-packages { max-width: 240px; }
  .merged-packages span { max-width: 150px; }
}
</style>
