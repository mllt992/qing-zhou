<template>
  <div>
    <div class="detail-head">
      <n-button size="small" @click="back">返回</n-button>
      <h2 class="page-title">
        <span class="status-beacon" :class="server?.status || ''" />
        {{ server?.name || '加载中…' }}
      </h2>
      <n-tag v-if="server?.location" size="small" :bordered="false">{{ server.location }}</n-tag>
    </div>
    <p class="page-sub">实时资源与趋势。往下的「流量状态」可以按某一时段回看整机进出流量、在线连接数，以及探针有没有断过。</p>

    <n-spin :show="loading">
      <template v-if="server">
        <!-- 资产信息条 -->
        <div class="asset-strip">
          <span v-if="server.provider" class="chip">{{ server.provider }}</span>
          <span v-if="server.spec" class="chip">{{ server.spec }}</span>
          <span v-if="server.price" class="chip">¥{{ server.price }}/月</span>
          <span v-if="server.days_left != null" class="chip" :class="{ danger: server.days_left <= 7 }">剩余 {{ server.days_left }} 天</span>
          <span v-if="server.metrics" class="chip">运行 {{ fmtUptime(server.metrics.uptime) }}</span>
          <span v-if="server.local" class="chip">面板内置采集 {{ server.probe_version }}</span>
          <span v-else-if="server.metrics" class="chip" :class="{ danger: server.probe_outdated }">
            探针 {{ server.probe_version || '版本未知' }}{{ server.probe_outdated ? ` · 待升级至 ${server.probe_target_version}` : '' }}
          </span>
        </div>

        <!-- 实时指标卡 -->
        <div v-if="server.metrics" class="metric-grid">
          <div class="metric-card">
            <span class="m-label">CPU</span>
            <span class="m-val" :class="pctClass(server.metrics.cpu_percent)">{{ server.metrics.cpu_percent.toFixed(1) }}%</span>
            <n-progress type="line" :percentage="server.metrics.cpu_percent" :show-indicator="false" :height="6" :color="pctColor(server.metrics.cpu_percent)" />
          </div>
          <div class="metric-card">
            <span class="m-label">内存</span>
            <span class="m-val" :class="pctClass(memPct)">{{ memPct.toFixed(1) }}%</span>
            <span class="m-sub">{{ fmtBytes(server.metrics.mem_used) }} / {{ fmtBytes(server.metrics.mem_total) }}</span>
            <n-progress type="line" :percentage="memPct" :show-indicator="false" :height="6" :color="pctColor(memPct)" />
          </div>
          <div class="metric-card">
            <span class="m-label">磁盘</span>
            <span class="m-val" :class="pctClass(diskPct)">{{ diskPct.toFixed(1) }}%</span>
            <span class="m-sub">{{ fmtBytes(server.metrics.disk_used) }} / {{ fmtBytes(server.metrics.disk_total) }}</span>
            <n-progress type="line" :percentage="diskPct" :show-indicator="false" :height="6" :color="pctColor(diskPct)" />
          </div>
          <div class="metric-card">
            <span class="m-label">网络上行</span>
            <span class="m-val">{{ fmtBytes(server.metrics.net_tx) }}/s</span>
          </div>
          <div class="metric-card">
            <span class="m-label">网络下行</span>
            <span class="m-val">{{ fmtBytes(server.metrics.net_rx) }}/s</span>
          </div>
          <div class="metric-card">
            <span class="m-label">所选区间总流量（IN + OUT）</span>
            <span class="m-val">{{ trafficStatus }}</span>
            <span class="m-sub">IN {{ trafficReady ? fmtBytes(trafficUsage.rx) : '—' }} / OUT {{ trafficReady ? fmtBytes(trafficUsage.tx) : '—' }}</span>
          </div>
          <div class="metric-card">
            <span class="m-label">系统负载</span>
            <span class="m-val">{{ server.metrics.load1.toFixed(2) }}</span>
            <span class="m-sub">{{ server.metrics.load5.toFixed(2) }} / {{ server.metrics.load15.toFixed(2) }}</span>
          </div>
        </div>

        <!-- 大图趋势 -->
        <n-card size="small" style="margin-top:16px;">
          <div class="chart-toolbar">
            <n-radio-group v-model:value="range" size="small" @update:value="loadChart">
              <n-radio-button v-for="r in ranges" :key="r.value" :value="r.value">{{ r.label }}</n-radio-button>
            </n-radio-group>
            <n-checkbox-group v-model:value="series" size="small" @update:value="drawChart">
              <n-space :size="4">
                <n-checkbox value="cpu">CPU</n-checkbox>
                <n-checkbox value="mem">内存</n-checkbox>
                <n-checkbox value="net">网络</n-checkbox>
                <n-checkbox value="disk">磁盘</n-checkbox>
                <n-checkbox value="load">负载</n-checkbox>
              </n-space>
            </n-checkbox-group>
          </div>
          <div ref="chartEl" class="big-chart" />
        </n-card>

        <!-- 流量状态：整机网卡计数，按时间段回看。不是代理用户流量。 -->
        <n-card id="traffic-status" size="small" style="margin-top:16px;">
          <div class="chart-toolbar">
            <span class="status-title">流量状态</span>
            <n-radio-group v-model:value="statusPreset" size="small" @update:value="onStatusPreset">
              <n-radio-button v-for="r in statusPresets" :key="r.value" :value="r.value">{{ r.label }}</n-radio-button>
            </n-radio-group>
            <n-date-picker
              v-if="statusPreset === 'custom'"
              v-model:value="statusCustom"
              type="datetimerange"
              size="small"
              clearable
              :is-date-disabled="disableStatusDate"
              style="width:340px;"
              @update:value="onStatusCustom"
            />
          </div>
          <p class="status-note">读的是探针已经存下的整机网卡计数和连接数，大约每分钟一条。不统计代理用户流量，查的时候也不会再去机器上抓。</p>
          <n-spin :show="statusLoading">
            <n-empty v-if="statusReady && !hasStatusSamples && !statusSilent" description="该时间段还没有数据" style="padding:28px 0;" />
            <n-empty v-else-if="statusReady && statusSilent" description="该时间段探针没有上报，看不到当时的流量和连接数" style="padding:28px 0;" />
            <template v-else-if="statusReady && hasStatusSamples">
              <div class="metric-grid">
                <div class="metric-card">
                  <span class="m-label">下行（入站）</span>
                  <span class="m-val">{{ statusBytes(status.rx_bytes) }}</span>
                  <span class="m-sub">{{ statusBucketText }}合计</span>
                </div>
                <div class="metric-card">
                  <span class="m-label">上行（出站）</span>
                  <span class="m-val">{{ statusBytes(status.tx_bytes) }}</span>
                  <span class="m-sub">网卡发出</span>
                </div>
                <div class="metric-card">
                  <span class="m-label">在线连接数峰值</span>
                  <span class="m-val">{{ status.peak_connections }}</span>
                  <span class="m-sub">已建立的 TCP 连接</span>
                </div>
                <div class="metric-card">
                  <span class="m-label">探针中断</span>
                  <span class="m-val" :class="status.gaps?.length ? 'warn' : 'ok'">{{ status.gaps?.length || 0 }} 次</span>
                  <span class="m-sub">{{ status.gaps?.length ? '中断期间不补 0' : '这段时间一直有上报' }}</span>
                </div>
              </div>
              <p v-if="!statusHasBytes" class="status-note">这段记录还算不出进出流量（没有相邻的网卡累计计数，常见于探针过旧或窗口里只有一条）。连接数和中断仍然是实数。</p>
              <p v-else class="status-note">柱子是这一档里的实际字节，不是瞬时速率。探针中断结束后的第一档，包含中断期间累计的流量。</p>
              <div ref="statusChartEl" class="big-chart" />
              <ul v-if="status.gaps?.length" class="gap-list">
                <li v-for="(g, i) in status.gaps" :key="i">
                  {{ fmtDateTime(g.from) }} – {{ fmtDateTime(g.to) }} 探针没有上报（{{ fmtGap(g.seconds) }}）
                </li>
              </ul>
            </template>
          </n-spin>
        </n-card>
      </template>
      <n-empty v-else-if="!loading" description="服务器不存在" style="padding:60px 0;" />
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NSpin, NCard, NButton, NTag, NProgress, NRadioGroup, NRadioButton, NCheckboxGroup, NCheckbox, NSpace, NEmpty,
  NDatePicker, useMessage
} from 'naive-ui'
import { apiGet } from '@/api'
import { fmtBytes, fmtUptime, fmtDateTime, pct } from '@/utils/format'
import * as echarts from 'echarts'

const route = useRoute()
const router = useRouter()
const sid = Number(route.params.id)

const loading = ref(true)
const server = ref<any>(null)
const range = ref('24h')
const series = ref(['cpu', 'mem', 'net'])
const chartEl = ref<HTMLElement | null>(null)
const trafficUsage = ref<any>({})
let chart: echarts.ECharts | null = null
let metrics: any[] = []
let resizeObs: ResizeObserver | null = null

const ranges = [
  { label: '1h', value: '1h' }, { label: '6h', value: '6h' },
  { label: '24h', value: '24h' }, { label: '7d', value: '7d' }, { label: '30d', value: '30d' },
]

const memPct = computed(() => server.value?.metrics ? pct(server.value.metrics.mem_used, server.value.metrics.mem_total) : 0)
const diskPct = computed(() => server.value?.metrics ? pct(server.value.metrics.disk_used, server.value.metrics.disk_total) : 0)
const trafficReady = computed(() => (trafficUsage.value?.sample_count || 0) >= 2)
const trafficStatus = computed(() => {
  if (trafficReady.value) return fmtBytes(trafficUsage.value.total)
  return server.value?.metrics?.net_totals_valid ? '采集中' : '需升级探针'
})

function pctClass(v: number) { return v >= 90 ? 'crit' : v >= 70 ? 'warn' : 'ok' }
function pctColor(v: number) { return v >= 90 ? '#c2685c' : v >= 70 ? '#bf9540' : '#6f8f76' }
function back() { router.push({ name: 'admin-monitor' }) }

async function loadServer() {
  try {
    const list = await apiGet<any[]>('/api/admin/monitor/servers')
    server.value = (list || []).find((s: any) => s.id === sid)
  } catch {}
}

async function loadChart() {
  try {
    const data = await apiGet<any>(`/api/admin/monitor/servers/${sid}/metrics?range=${range.value}`)
    metrics = data?.data || []
    trafficUsage.value = data?.traffic_usage || {}
    await nextTick()
    drawChart()
  } catch {}
}

function drawChart() {
  if (!chartEl.value) return
  if (!chart) chart = echarts.init(chartEl.value)
  const times = metrics.map((m: any) => {
    const d = new Date(m.ts * 1000)
    return range.value === '1h' || range.value === '6h'
      ? `${d.getHours()}:${String(d.getMinutes()).padStart(2, '0')}`
      : `${d.getMonth() + 1}/${d.getDate()} ${d.getHours()}:00`
  })
  const s: any[] = []
  if (series.value.includes('cpu')) s.push({ name: 'CPU %', type: 'line', smooth: true, showSymbol: false, lineStyle: { width: 1.5 }, data: metrics.map(m => m.cpu_percent?.toFixed(1)) })
  if (series.value.includes('mem')) s.push({ name: '内存 %', type: 'line', smooth: true, showSymbol: false, lineStyle: { width: 1.5 }, data: metrics.map(m => pct(m.mem_used, m.mem_total).toFixed(1)) })
  if (series.value.includes('disk')) s.push({ name: '磁盘 %', type: 'line', smooth: true, showSymbol: false, lineStyle: { width: 1.5 }, data: metrics.map(m => pct(m.disk_used, m.disk_total).toFixed(1)) })
  if (series.value.includes('net')) {
    s.push({ name: '上行 MB/s', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false, lineStyle: { width: 1 }, data: metrics.map(m => ((m.net_tx || 0) / 1048576).toFixed(2)) })
    s.push({ name: '下行 MB/s', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false, lineStyle: { width: 1 }, data: metrics.map(m => ((m.net_rx || 0) / 1048576).toFixed(2)) })
  }
  if (series.value.includes('load')) s.push({ name: '负载', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false, lineStyle: { width: 1 }, data: metrics.map(m => m.load1?.toFixed(2)) })

  chart.setOption({
    tooltip: { trigger: 'axis' },
    legend: { top: 0, textStyle: { fontSize: 11 } },
    grid: { left: 44, right: 48, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: times, axisLabel: { fontSize: 10 } },
    yAxis: [
      { type: 'value', name: '%', max: 100, axisLabel: { fontSize: 10 } },
      { type: 'value', name: 'MB/s', axisLabel: { fontSize: 10 } },
    ],
    series: s,
  }, true)
}


const message = useMessage()
const statusPreset = ref('24h')
const statusCustom = ref<[number, number] | null>(null)
const statusLoading = ref(false)
const statusReady = ref(false)
const status = ref<any>(null)
const statusChartEl = ref<HTMLElement | null>(null)
let statusChart: echarts.ECharts | null = null
const statusPresets = [
  { label: '近 1 小时', value: '1h' },
  { label: '近 6 小时', value: '6h' },
  { label: '近 24 小时', value: '24h' },
  { label: '近 7 天', value: '7d' },
  { label: '近 30 天', value: '30d' },
  { label: '自定义', value: 'custom' },
]
const hasStatusSamples = computed(() => (status.value?.sample_count || 0) > 0)
const statusSilent = computed(() => !hasStatusSamples.value && (status.value?.gaps?.length || 0) > 0)
const statusHasBytes = computed(() => (status.value?.delta_samples || 0) > 0)
const statusBucketText = computed(() => {
  const sec = status.value?.bucket_sec || 60
  if (sec <= 60) return '每分钟'
  if (sec < 3600) return `每 ${Math.round(sec / 60)} 分钟`
  return `每 ${Math.round(sec / 3600)} 小时`
})

function statusBytes(n: number) {
  return statusHasBytes.value ? fmtBytes(n) : '—'
}
function fmtGap(sec: number) {
  if (!sec || sec < 60) return `${sec || 0} 秒`
  const m = Math.round(sec / 60)
  if (m < 60) return `${m} 分钟`
  const h = Math.floor(m / 60)
  const rm = m % 60
  return rm ? `${h} 小时 ${rm} 分钟` : `${h} 小时`
}
function disableStatusDate(ts: number) {
  const now = Date.now()
  return ts > now || ts < now - 35 * 86400000
}
function statusBounds(): { from: number, to: number } | null {
  const now = Math.floor(Date.now() / 1000)
  const span: Record<string, number> = {
    '1h': 3600, '6h': 6 * 3600, '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400,
  }
  if (statusPreset.value !== 'custom') {
    return { from: now - span[statusPreset.value], to: now }
  }
  const r = statusCustom.value
  if (!r || !r[0] || !r[1] || r[1] <= r[0]) return null
  return { from: Math.floor(r[0] / 1000), to: Math.floor(r[1] / 1000) }
}
function onStatusPreset(v: string) {
  if (v !== 'custom') loadStatus()
}
function onStatusCustom(v: [number, number] | null) {
  if (v && v[0] && v[1]) loadStatus()
}

async function loadStatus() {
  const bounds = statusBounds()
  if (!bounds) return
  statusLoading.value = true
  try {
    status.value = await apiGet<any>(`/api/admin/monitor/servers/${sid}/traffic-status?from=${bounds.from}&to=${bounds.to}`)
    statusReady.value = true
    await nextTick()
    drawStatus()
  } catch (e: any) {
    statusReady.value = true
    message.error(e?.message || '查询流量状态失败')
  } finally {
    statusLoading.value = false
  }
}

function drawStatus() {
  if (!hasStatusSamples.value) {
    statusChart?.dispose()
    statusChart = null
    return
  }
  if (!statusChartEl.value) return
  if (!statusChart) statusChart = echarts.init(statusChartEl.value)
  const gaps = status.value?.gaps || []
  const points = status.value?.points || []
  const broken = (ts: number, prev: number) => gaps.some((g: any) => g.from < ts && g.to > prev)
  const traffic = (key: 'rx_bytes' | 'tx_bytes') => {
    const out: any[] = []
    let prev = 0
    for (const p of points) {
      if (prev && broken(p.ts, prev)) out.push([p.ts * 1000 - 1000, null])
      out.push([p.ts * 1000, p.totals_valid ? p[key] : null])
      prev = p.ts
    }
    return out
  }
  const conns: any[] = []
  {
    let prev = 0
    for (const p of points) {
      if (prev && broken(p.ts, prev)) conns.push([p.ts * 1000 - 1000, null])
      conns.push([p.ts * 1000, p.connections])
      prev = p.ts
    }
  }
  const showBytes = statusHasBytes.value
  const series: any[] = []
  if (showBytes) {
    series.push({
      name: '下行', type: 'bar', yAxisIndex: 0, barMaxWidth: 14,
      itemStyle: { color: '#3d7ea6' }, data: traffic('rx_bytes'),
    })
    series.push({
      name: '上行', type: 'bar', yAxisIndex: 0, barMaxWidth: 14,
      itemStyle: { color: '#6f8f76' }, data: traffic('tx_bytes'),
    })
  }
  series.push({
    name: '连接数', type: 'line', yAxisIndex: showBytes ? 1 : 0, smooth: false, showSymbol: false,
    connectNulls: false, lineStyle: { width: 1.5, color: '#bf9540' }, itemStyle: { color: '#bf9540' },
    data: conns,
    markArea: {
      silent: true,
      itemStyle: { color: 'rgba(194, 104, 92, 0.14)' },
      label: { color: '#c2685c', fontSize: 11 },
      data: gaps.map((g: any) => [
        { name: '探针中断', xAxis: g.from * 1000 },
        { xAxis: g.to * 1000 },
      ]),
    },
  })
  statusChart.setOption({
    tooltip: {
      trigger: 'axis',
      formatter(items: any) {
        const list = Array.isArray(items) ? items : [items]
        const head = fmtDateTime(Math.floor((list[0]?.axisValue || 0) / 1000))
        const lines = list.map((it: any) => {
          const v = it.value?.[1]
          if (v == null) return `${it.marker}${it.seriesName}：无数据`
          const shown = it.seriesName === '连接数' ? String(v) : fmtBytes(v)
          return `${it.marker}${it.seriesName}：${shown}`
        })
        return [head, ...lines].join('<br/>')
      },
    },
    legend: { top: 0, textStyle: { fontSize: 11 } },
    grid: { left: 56, right: showBytes ? 48 : 24, top: 32, bottom: 28 },
    xAxis: {
      type: 'time',
      min: (status.value?.from || 0) * 1000,
      max: (status.value?.to || 0) * 1000,
      axisLabel: { fontSize: 10 },
    },
    yAxis: showBytes
      ? [
          { type: 'value', name: '字节', axisLabel: { fontSize: 10, formatter: (v: number) => fmtBytes(v) } },
          { type: 'value', name: '连接', minInterval: 1, axisLabel: { fontSize: 10 } },
        ]
      : [{ type: 'value', name: '连接', minInterval: 1, axisLabel: { fontSize: 10 } }],
    series,
  }, true)
  statusChart.resize()
}

watch(statusChartEl, () => { if (statusChartEl.value) drawStatus() })

function applyStatusQuery() {
  const from = Number(route.query.from)
  const to = Number(route.query.to)
  if (from > 1e9 && to > from) {
    statusPreset.value = 'custom'
    statusCustom.value = [from * 1000, to * 1000]
  }
}

onMounted(async () => {
  applyStatusQuery()
  loading.value = true
  await loadServer()
  loading.value = false
  await nextTick()
  await loadChart()
  await loadStatus()
  if (route.hash === '#traffic-status') {
    await nextTick()
    document.getElementById('traffic-status')?.scrollIntoView({ block: 'start' })
  }
  // 监听容器尺寸变化，自动 resize 图表
  if (chartEl.value && typeof ResizeObserver !== 'undefined') {
    resizeObs = new ResizeObserver(() => {
      chart?.resize()
      statusChart?.resize()
    })
    resizeObs.observe(chartEl.value)
  }
})
onUnmounted(() => {
  resizeObs?.disconnect()
  chart?.dispose()
  statusChart?.dispose()
})
</script>

<style scoped>
.detail-head { display: flex; align-items: center; gap: 12px; margin-bottom: 14px; }
.page-title { font-size: 20px; margin: 0; display: flex; align-items: center; gap: 8px; }
.status-beacon { width: 9px; height: 9px; border-radius: 50%; flex-shrink: 0; }
.status-beacon.online { background: #10b981; box-shadow: 0 0 8px rgba(16,185,129,.5); }
.status-beacon.offline { background: #ef4444; }

.asset-strip { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 16px; }
.chip { padding: 3px 10px; border-radius: 7px; background: var(--bg-soft); font-size: 12px; color: var(--text-2); }
.chip.danger { background: var(--danger-soft); color: var(--danger); }

.metric-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 12px; }
.metric-card { display: flex; flex-direction: column; gap: 4px; padding: 14px; background: var(--card); border: 1px solid var(--border); border-radius: 12px; }
.m-label { font-size: 11px; color: var(--text-3); font-weight: 600; text-transform: uppercase; letter-spacing: .05em; }
.m-val { font-size: 22px; font-weight: 720; }
.m-val.ok { color: var(--accent-strong); }
.m-val.warn { color: var(--warn); }
.m-val.crit { color: var(--danger); }
.m-sub { font-size: 11px; color: var(--text-3); }

.chart-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 12px; }
.status-title { font-size: 14px; font-weight: 700; }
.status-note { margin: 0 0 12px; font-size: 12px; color: var(--text-3); line-height: 1.5; }
.gap-list { margin: 8px 0 0; padding-left: 18px; font-size: 12px; color: var(--text-2); }
.gap-list li { margin: 4px 0; }
.big-chart { height: 380px; }
@media (max-width: 768px) { .big-chart { height: 280px; } }
</style>
