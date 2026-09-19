<template>
  <div class="monitor-page">
    <AppHeader />

    <!-- SVG 渐变定义（供仪表盘 / 迷你图引用） -->
    <svg width="0" height="0" style="position:absolute" aria-hidden="true">
      <defs>
        <linearGradient id="gg-ok" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stop-color="#7fa588" /><stop offset="1" stop-color="#5c7c63" />
        </linearGradient>
        <linearGradient id="gg-warn" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stop-color="#e8c069" /><stop offset="1" stop-color="#c99728" />
        </linearGradient>
        <linearGradient id="gg-crit" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stop-color="#d98a7f" /><stop offset="1" stop-color="#c2685c" />
        </linearGradient>
        <linearGradient id="spark-grad" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color="#3180b5" stop-opacity="0.24" />
          <stop offset="1" stop-color="#3180b5" stop-opacity="0" />
        </linearGradient>
      </defs>
    </svg>

    <div class="monitor-content">
      <!-- 自定义首页模式 -->
      <template v-if="config.config.homepage_mode === 'custom' && config.config.homepage_url">
        <iframe
          :src="config.config.homepage_url"
          style="width: 100%; height: calc(100vh - 56px); border: none;"
          sandbox="allow-scripts allow-same-origin allow-popups"
        />
      </template>

      <!-- 监控大屏模式 -->
      <template v-else>
        <!-- 顶部标题栏 -->
        <div class="hero">
          <div class="hero-left">
            <h1 class="hero-title">服务器监控</h1>
            <p class="hero-sub">实时掌握每台节点的运行状态</p>
          </div>
          <div class="hero-right">
            <div class="clock">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><polyline points="12 7 12 12 15 14"/></svg>
              {{ clock }}
            </div>
            <div class="auto-badge">
              <span class="auto-dot" :class="{ active: refreshing }"></span>
              自动刷新 · 30s
            </div>
            <button class="refresh-btn" :class="{ spinning: refreshing }" @click="doRefresh" :disabled="refreshing" title="立即刷新">
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
            </button>
          </div>
        </div>

        <!-- 汇总卡片 -->
        <div class="summary-grid">
          <div class="summary-card">
            <div class="summary-icon i-server">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><line x1="6" y1="7" x2="6.01" y2="7"/><line x1="6" y1="17" x2="6.01" y2="17"/></svg>
            </div>
            <div class="summary-body">
              <span class="summary-val">{{ Math.round(dTotal) }}</span>
              <span class="summary-label">服务器 · <b class="ok-text">{{ Math.round(dOnline) }} 在线</b><template v-if="offlineCount"> · <b class="crit-text">{{ offlineCount }} 离线</b></template></span>
            </div>
          </div>

          <div class="summary-card">
            <div class="summary-icon i-cpu">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><line x1="9" y1="1" x2="9" y2="4"/><line x1="15" y1="1" x2="15" y2="4"/><line x1="9" y1="20" x2="9" y2="23"/><line x1="15" y1="20" x2="15" y2="23"/><line x1="20" y1="9" x2="23" y2="9"/><line x1="20" y1="14" x2="23" y2="14"/><line x1="1" y1="9" x2="4" y2="9"/><line x1="1" y1="14" x2="4" y2="14"/></svg>
            </div>
            <div class="summary-body">
              <span class="summary-val">{{ hasData ? dCpu.toFixed(1) : '—' }}<i>%</i></span>
              <span class="summary-label">平均 CPU</span>
              <div class="mini-bar"><div class="mini-fill" :class="lvl(avgCpuN)" :style="{ width: Math.min(avgCpuN,100)+'%' }" /></div>
            </div>
          </div>

          <div class="summary-card">
            <div class="summary-icon i-mem">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="7" width="20" height="10" rx="2"/><line x1="6" y1="11" x2="6" y2="13"/><line x1="10" y1="11" x2="10" y2="13"/><line x1="14" y1="11" x2="14" y2="13"/><line x1="18" y1="11" x2="18" y2="13"/></svg>
            </div>
            <div class="summary-body">
              <span class="summary-val">{{ hasData ? dMem.toFixed(1) : '—' }}<i>%</i></span>
              <span class="summary-label">平均内存</span>
              <div class="mini-bar"><div class="mini-fill" :class="lvl(avgMemN)" :style="{ width: Math.min(avgMemN,100)+'%' }" /></div>
            </div>
          </div>

          <div class="summary-card">
            <div class="summary-icon i-disk">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="3"/></svg>
            </div>
            <div class="summary-body">
              <span class="summary-val">{{ hasData ? dDisk.toFixed(1) : '—' }}<i>%</i></span>
              <span class="summary-label">平均磁盘</span>
              <div class="mini-bar"><div class="mini-fill" :class="lvl(avgDiskN)" :style="{ width: Math.min(avgDiskN,100)+'%' }" /></div>
            </div>
          </div>

          <div class="summary-card">
            <div class="summary-icon i-up">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></svg>
            </div>
            <div class="summary-body">
              <span class="summary-val small">{{ fmtBytes(dUp) }}<i>/s</i></span>
              <span class="summary-label">总上行</span>
            </div>
          </div>

          <div class="summary-card">
            <div class="summary-icon i-down">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><polyline points="19 12 12 19 5 12"/></svg>
            </div>
            <div class="summary-body">
              <span class="summary-val small">{{ fmtBytes(dDown) }}<i>/s</i></span>
              <span class="summary-label">总下行</span>
            </div>
          </div>
        </div>

        <!-- 刷新 / 热力图范围 -->
        <div class="section-bar">
          <span class="section-title">可用性热力图</span>
          <div class="heat-range">
            <button v-for="r in heatRanges" :key="r.value" class="heat-range-btn" :class="{ active: heatRange === r.value }" @click="loadHeatmap(r.value)">{{ r.label }}</button>
            <span class="heat-legend"><i class="hm-dot ok"></i>正常 <i class="hm-dot warn"></i>高负载 <i class="hm-dot crit"></i>严重 <i class="hm-dot none"></i>无数据</span>
          </div>
        </div>

        <!-- 可用性热力图 -->
        <div class="heatmap-card">
          <div ref="heatEl" class="heat-chart"></div>
          <div v-if="!heatData?.servers?.length" class="heat-empty">暂无探针服务器</div>
        </div>

        <!-- 空状态 -->
        <n-empty v-if="!loading && homepageCards.length === 0" description="暂无服务器数据" style="padding: 80px 0;" />

        <!-- 服务器卡片 -->
        <div class="server-grid">
          <template v-for="(s, i) in homepageCards" :key="s.name">
          <!-- 上游余额独立于面板本机：管理员可在上游管理页开关，不依赖本机卡片是否公开。 -->
          <div v-if="s.name === UPSTREAM_BALANCE_CARD" class="server-card upstream-balance-card" :style="{ '--i': i }">
            <div class="card-top-line online" />
            <div class="card-header">
              <div class="card-title">
                <span class="status-beacon online" />
                <span class="server-name">上游余额</span>
              </div>
              <span class="status-badge online"><i class="badge-dot" /> 官方数据</span>
            </div>
            <div class="tag-line">
              <span class="tag loc">OCI · Cloudflare</span>
              <span class="tag spec">每 15 分钟更新</span>
            </div>
            <div class="upstream-balance-grid">
              <div
                v-for="item in upstreamBalanceItems"
                :key="item.provider"
                class="upstream-balance-item"
                :class="{ dragging: upstreamDragging === item.provider, 'drag-over': upstreamDragOver === item.provider }"
                draggable="true"
                @dragstart="handleUpstreamDragStart(item.provider, $event)"
                @dragover.prevent="handleUpstreamDragOver(item.provider, $event)"
                @drop.prevent="handleUpstreamDrop(item.provider)"
                @dragend="handleUpstreamDragEnd"
              >
                <div class="upstream-balance-head">
                  <span class="upstream-provider-mark" :class="item.provider">{{ item.provider === 'oci' ? 'OCI' : 'CF' }}</span>
                  <span class="upstream-provider-name">{{ item.provider === 'oci' ? 'Oracle Cloud' : 'Cloudflare' }}</span>
                  <span class="upstream-drag-hint" title="拖动调整余额顺序">⋮⋮</span>
                </div>
                <template v-if="item.usage?.success">
                  <div class="upstream-balance-value">{{ upstreamBalanceValue(item) }}</div>
                  <div class="upstream-balance-meta">{{ upstreamBalanceMeta(item) }}</div>
                  <div class="upstream-balance-track"><i :class="upstreamUsageLevel(item.usage)" :style="{ width: upstreamUsagePercent(item.usage) + '%' }" /></div>
                  <div class="upstream-balance-foot">{{ item.usage.period || '当前周期' }} · {{ fmtUpdated(item.usage.updated_at) }}</div>
                </template>
                <template v-else>
                  <div class="upstream-balance-value muted">{{ item.view.configured ? '查询中' : '未配置' }}</div>
                  <div class="upstream-balance-meta">{{ item.view.configured ? (item.usage?.error || '等待官方接口返回') : '前往上游管理配置' }}</div>
                  <div class="upstream-balance-foot">{{ item.provider === 'oci' ? 'OCI Usage API' : 'Cloudflare Analytics GraphQL' }}</div>
                </template>
              </div>
            </div>
            <div class="card-footer upstream-card-footer">
              <span class="footer-time"><span class="dot online" />余额卡片可拖动排序</span>
              <button class="upstream-refresh-link" type="button" :disabled="upstreamRefreshing" @click="refreshUpstreamBalances">刷新</button>
            </div>
          </div>
          <div v-else class="server-card" :style="{ '--i': i }">
            <div class="card-top-line" :class="s.status" />

            <!-- 头部 -->
            <div class="card-header">
              <div class="card-title">
                <span class="status-beacon" :class="s.status" />
                <span class="server-name" :title="s.name">{{ s.name }}</span>
              </div>
              <span class="status-badge" :class="s.status">
                <i class="badge-dot" /> {{ s.status === 'online' ? '运行中' : '离线' }}
              </span>
            </div>

            <!-- 标签 -->
            <div class="tag-line">
              <span v-if="s.location" class="tag loc">{{ s.location }}</span>
              <span v-if="s.provider" class="tag">{{ s.provider }}</span>
              <span v-if="s.spec" class="tag spec">{{ s.spec }}</span>
              <span v-if="s.price != null" class="tag price">¥{{ Number(s.price).toFixed(2) }}/月</span>
              <span v-if="s.days_left != null" class="tag expiry" :class="expiryCls(s.days_left)" title="距离到期剩余天数">
                <i class="exp-dot" />剩 {{ s.days_left }} 天
              </span>
            </div>

            <template v-if="s.metrics">
              <!-- 三仪表盘 -->
              <div class="gauges">
                <div class="gauge" v-for="g in gauges(s)" :key="g.key">
                  <svg viewBox="0 0 64 64" class="gauge-svg">
                    <circle class="gauge-bg" cx="32" cy="32" r="26" />
                    <circle class="gauge-fg" cx="32" cy="32" r="26"
                      :stroke="`url(#gg-${g.lvl})`"
                      :stroke-dasharray="GAUGE_C"
                      :stroke-dashoffset="g.off" />
                  </svg>
                  <div class="gauge-center">
                    <span class="gauge-val" :class="g.lvl">{{ g.val.toFixed(0) }}<i>%</i></span>
                  </div>
                  <div class="gauge-meta">
                    <span class="gauge-label">{{ g.key }}</span>
                    <span class="gauge-sub">{{ g.sub }}</span>
                  </div>
                </div>
              </div>

              <!-- CPU 迷你趋势图 -->
              <div v-if="s.spark && s.spark.cpu && s.spark.cpu.length" class="spark-wrap">
                <svg class="spark" :viewBox="`0 0 100 ${SPARK_H}`" preserveAspectRatio="none">
                  <path :d="sparkArea(s.spark.cpu)" class="spark-area" />
                  <path :d="sparkLine(s.spark.cpu)" class="spark-line" />
                </svg>
                <span class="spark-tag">CPU · 近 1 小时</span>
              </div>

              <!-- 交换分区（有才显示） -->
              <div v-if="s.metrics.swap_total > 0" class="swap-row">
                <span class="swap-label">交换</span>
                <div class="swap-track"><div class="swap-fill" :class="lvl(swapPct(s))" :style="{ width: Math.min(swapPct(s),100)+'%' }" /></div>
                <span class="swap-detail">{{ fmtBytes(s.metrics.swap_used) }} / {{ fmtBytes(s.metrics.swap_total) }}</span>
              </div>

              <!-- 详细信息网格 -->
              <div class="info-grid">
                <div class="info-cell" title="实时上行速率">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="19" x2="12" y2="5"/><polyline points="5 12 12 5 19 12"/></svg>
                  <div><span class="ic-label">上行</span><span class="ic-val">{{ fmtBytes(s.metrics.net_up) }}/s</span></div>
                </div>
                <div class="info-cell" title="实时下行速率">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><polyline points="19 12 12 19 5 12"/></svg>
                  <div><span class="ic-label">下行</span><span class="ic-val">{{ fmtBytes(s.metrics.net_down) }}/s</span></div>
                </div>
                <div class="info-cell" :title="`1 / 5 / 15 分钟平均负载`">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20V10"/><path d="M18 20V4"/><path d="M6 20v-4"/></svg>
                  <div><span class="ic-label">负载</span><span class="ic-val">{{ s.metrics.load1.toFixed(2) }} / {{ s.metrics.load5.toFixed(2) }} / {{ s.metrics.load15.toFixed(2) }}</span></div>
                </div>
                <div class="info-cell" title="运行时长">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><polyline points="12 7 12 12 15 14"/></svg>
                  <div><span class="ic-label">运行</span><span class="ic-val">{{ fmtUptime(s.metrics.uptime) }}</span></div>
                </div>
                <div class="info-cell" v-if="s.metrics.tcp_connections != null" title="TCP 连接数">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 6l6 0"/><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><path d="M6 8.5v7a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2v-7"/><circle cx="12" cy="19" r="2.5"/></svg>
                  <div><span class="ic-label">连接</span><span class="ic-val">{{ s.metrics.tcp_connections }}</span></div>
                </div>
                <div class="info-cell" v-if="s.metrics.process_count != null" title="进程数">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></svg>
                  <div><span class="ic-label">进程</span><span class="ic-val">{{ s.metrics.process_count }}</span></div>
                </div>
                <div class="info-cell wide" v-if="s.metrics.platform" title="操作系统 / 架构">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>
                  <div><span class="ic-label">系统</span><span class="ic-val ellip">{{ s.metrics.platform }}<template v-if="s.metrics.arch"> · {{ s.metrics.arch }}</template></span></div>
                </div>
              </div>
            </template>

            <div v-else class="card-no-data">
              <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" opacity=".3"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
              <span>暂无数据</span>
            </div>

            <div v-if="s.traffic" class="public-traffic">
              <div class="public-traffic-head">
                <span>本周期流量 · {{ trafficModeLabel(s.traffic.accounting_mode) }}</span>
                <b>{{ trafficReady(s.traffic) ? fmtBytes(s.traffic.used_bytes) : '采集中' }}<template v-if="s.traffic.limit_bytes > 0"> / {{ fmtBytes(s.traffic.limit_bytes) }}</template></b>
              </div>
              <div v-if="s.traffic.limit_bytes > 0" class="public-traffic-track"><i :class="lvl(trafficPct(s.traffic))" :style="{ width: Math.min(trafficPct(s.traffic), 100) + '%' }" /></div>
              <div class="public-traffic-meta">
                <span>下次重置 {{ fmtShortDate(s.traffic.next_reset) }}</span>
                <span v-if="trafficIncomplete(s.traffic)" class="warn-text">数据自 {{ fmtShortDate(s.traffic.coverage_start, true) }} 起</span>
                <span v-else-if="s.traffic.calibrated">已按服务商用量校准</span>
              </div>
            </div>

            <div class="card-footer">
              <span class="footer-time">
                <span class="dot" :class="s.status" />
                最后更新 {{ timeAgo(s.last_seen) }}
              </span>
            </div>
          </div>
          </template>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, shallowRef, reactive, watch } from 'vue'
import { NEmpty } from 'naive-ui'
import { apiGet, apiPost, apiPut } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { fmtBytes, fmtUptime, timeAgo, pct } from '@/utils/format'
import { useCountUp } from '@/utils/countup'
import AppHeader from '@/components/AppHeader.vue'
import * as echarts from 'echarts'

interface ServerMetrics {
  cpu_percent: number; mem_used: number; mem_total: number
  swap_used: number; swap_total: number
  disk_used: number; disk_total: number
  net_up: number; net_down: number
  load1: number; load5: number; load15: number
  tcp_connections: number; process_count: number
  uptime: number; platform: string; arch: string
}
interface Spark { name: string; cpu: number[]; net_up: number[]; net_down: number[] }
interface PublicTraffic {
  used_bytes: number; limit_bytes: number; cycle_start: number; next_reset: number
  accounting_mode: string; sample_count: number; coverage_start: number; calibrated: boolean
}
interface Server {
  name: string; status: 'online' | 'offline'; location: string
  provider: string; spec: string; days_left?: number | null
  price?: number; traffic?: PublicTraffic
  metrics: ServerMetrics | null; last_seen: number; spark?: Spark | null
}

type UpstreamProvider = 'oci' | 'cloudflare'
interface UpstreamView {
  provider: UpstreamProvider
  configured: boolean
  limit: number
}
interface UpstreamUsage {
  configured: boolean
  success: boolean
  used: number
  limit: number
  remaining: number
  unit: string
  period: string
  source: string
  updated_at?: string
  error?: string
}

const config = useConfigStore()
const auth = useAuthStore()
const servers = ref<Server[]>([])
const loading = ref(false)
const refreshing = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const upstreamDefaults: UpstreamProvider[] = ['oci', 'cloudflare']
const upstreamOrder = ref<UpstreamProvider[]>([...upstreamDefaults])
const upstreamBalanceVisible = ref(false)
const UPSTREAM_BALANCE_CARD = '上游余额'
const homepageCards = computed(() => {
  const cards = [...servers.value]
  if (!auth.isAdmin || !upstreamBalanceVisible.value) return cards
  const localIdx = cards.findIndex(server => server.name === '面板本机')
  const insertAt = localIdx >= 0 ? localIdx + 1 : 0
  cards.splice(insertAt, 0, { name: UPSTREAM_BALANCE_CARD } as Server)
  return cards
})
const upstreamViews = reactive<Record<UpstreamProvider, UpstreamView>>({
  oci: { provider: 'oci', configured: false, limit: 10_000_000_000_000 },
  cloudflare: { provider: 'cloudflare', configured: false, limit: 100_000 },
})
const upstreamUsages = reactive<Partial<Record<UpstreamProvider, UpstreamUsage>>>({})
const upstreamLoading = ref(false)
const upstreamRefreshing = ref(false)
const upstreamDragging = ref<UpstreamProvider | null>(null)
const upstreamDragOver = ref<UpstreamProvider | null>(null)
let upstreamTimer: ReturnType<typeof setInterval> | null = null

const upstreamBalanceItems = computed(() => upstreamOrder.value.map(provider => ({
  provider,
  view: upstreamViews[provider],
  usage: upstreamUsages[provider],
})))

function normalizeUpstreamOrder(raw: unknown): UpstreamProvider[] {
  const values = Array.isArray(raw) ? raw : String(raw || '').split(',')
  const valid = values.filter((value): value is UpstreamProvider => value === 'oci' || value === 'cloudflare')
  return [...new Set([...valid, ...upstreamDefaults])]
}
function upstreamUsagePercent(usage?: UpstreamUsage) {
  return usage?.limit ? Math.min(100, Math.max(0, Math.round(usage.used / usage.limit * 1000) / 10)) : 0
}
function upstreamUsageLevel(usage?: UpstreamUsage) {
  const percent = upstreamUsagePercent(usage)
  return percent >= 90 ? 'crit' : percent >= 70 ? 'warn' : 'ok'
}
function upstreamBalanceValue(item: { provider: UpstreamProvider; usage?: UpstreamUsage }) {
  return item.provider === 'oci' ? fmtBytes(item.usage?.remaining || 0) : fmtRequests(item.usage?.remaining)
}
function upstreamBalanceMeta(item: { provider: UpstreamProvider; usage?: UpstreamUsage }) {
  if (!item.usage) return ''
  return item.provider === 'oci'
    ? `总额 ${fmtBytes(item.usage.limit)} − 已用 ${fmtBytes(item.usage.used)}`
    : `已用 ${fmtRequests(item.usage.used)} / 上限 ${fmtRequests(item.usage.limit)}`
}
function fmtRequests(value?: number) { return new Intl.NumberFormat('zh-CN').format(value || 0) + ' 次' }
function fmtUpdated(value?: string) { return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '尚未更新' }

async function refreshUpstream(provider: UpstreamProvider) {
  const view = upstreamViews[provider]
  if (!view.configured) {
    delete upstreamUsages[provider]
    return
  }
  try {
    upstreamUsages[provider] = await apiPost<UpstreamUsage>(`/api/admin/upstreams/${provider}/refresh`, undefined, { timeoutMs: 30_000 })
  } catch (error: any) {
    upstreamUsages[provider] = {
      configured: true, success: false, used: 0, limit: view.limit, remaining: 0,
      unit: provider === 'oci' ? 'bytes' : 'requests', period: '', source: '', error: error.message || '查询失败',
    }
  }
}
async function loadUpstreamBalances() {
  if (!auth.isAdmin || upstreamLoading.value) return
  upstreamLoading.value = true
  try {
    const [views, settings] = await Promise.all([
      apiGet<UpstreamView[]>('/api/admin/upstreams'),
      apiGet<Record<string, string>>('/api/admin/settings'),
    ])
    for (const provider of upstreamDefaults) {
      const view = views?.find(item => item.provider === provider)
      Object.assign(upstreamViews[provider], view || { provider, configured: false })
    }
    upstreamOrder.value = normalizeUpstreamOrder(settings?.admin_upstream_balance_order)
    upstreamBalanceVisible.value = settings?.admin_upstream_balance_visible === 'true'
    if (!upstreamBalanceVisible.value) return
    await Promise.all(upstreamDefaults.map(refreshUpstream))
  } catch {
    // The public monitor must remain usable when an administrator session expires
    // or a provider request is unavailable.
  } finally { upstreamLoading.value = false }
}
async function refreshUpstreamBalances() {
  if (!auth.isAdmin || !upstreamBalanceVisible.value || upstreamRefreshing.value) return
  upstreamRefreshing.value = true
  try { await Promise.all(upstreamDefaults.map(refreshUpstream)) } finally { upstreamRefreshing.value = false }
}
function handleUpstreamDragStart(provider: UpstreamProvider, event: DragEvent) {
  upstreamDragging.value = provider
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', provider)
  }
}
function handleUpstreamDragOver(provider: UpstreamProvider, event: DragEvent) {
  if (!upstreamDragging.value || upstreamDragging.value === provider) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  upstreamDragOver.value = provider
}
async function handleUpstreamDrop(provider: UpstreamProvider) {
  const source = upstreamDragging.value
  upstreamDragOver.value = null
  if (!source || source === provider) return
  const previous = [...upstreamOrder.value]
  const next = [...previous]
  const from = next.indexOf(source), to = next.indexOf(provider)
  if (from < 0 || to < 0) return
  const target = next.indexOf(provider)
  ;[next[from], next[target]] = [next[target], next[from]]
  upstreamOrder.value = next
  try {
    await apiPut('/api/admin/settings', { admin_upstream_balance_order: next.join(',') })
  } catch {
    upstreamOrder.value = previous
  }
}
function handleUpstreamDragEnd() {
  upstreamDragging.value = null
  upstreamDragOver.value = null
}

watch(() => auth.isAdmin, enabled => {
  if (enabled) void loadUpstreamBalances()
})

// 实时时钟
const clock = ref('')
let clockTimer: ReturnType<typeof setInterval> | null = null
function tickClock() {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  clock.value = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const onlineCount = computed(() => servers.value.filter(s => s.status === 'online').length)
const offlineCount = computed(() => servers.value.length - onlineCount.value)
const hasData = computed(() => servers.value.some(s => s.metrics))

const avgCpuN = computed(() => {
  const arr = servers.value.filter(s => s.metrics)
  if (!arr.length) return 0
  return arr.reduce((s, x) => s + (x.metrics?.cpu_percent || 0), 0) / arr.length
})
const avgMemN = computed(() => {
  const arr = servers.value.filter(s => s.metrics)
  const u = arr.reduce((s, x) => s + (x.metrics?.mem_used || 0), 0)
  const t = arr.reduce((s, x) => s + (x.metrics?.mem_total || 0), 0)
  return t ? (u / t) * 100 : 0
})
const avgDiskN = computed(() => {
  const arr = servers.value.filter(s => s.metrics)
  const u = arr.reduce((s, x) => s + (x.metrics?.disk_used || 0), 0)
  const t = arr.reduce((s, x) => s + (x.metrics?.disk_total || 0), 0)
  return t ? (u / t) * 100 : 0
})
const totalUp = computed(() => servers.value.reduce((s, x) => s + (x.metrics?.net_up || 0), 0))
const totalDown = computed(() => servers.value.reduce((s, x) => s + (x.metrics?.net_down || 0), 0))

// 数字滚动动画
// round:false —— CPU/内存/流量这些指标本身带小数，取整会把 0.4% 抹成 0%。
const NOROUND = { round: false }
const dTotal = useCountUp(() => servers.value.length, NOROUND)
const dOnline = useCountUp(() => onlineCount.value, NOROUND)
const dCpu = useCountUp(() => avgCpuN.value, NOROUND)
const dMem = useCountUp(() => avgMemN.value, NOROUND)
const dDisk = useCountUp(() => avgDiskN.value, NOROUND)
const dUp = useCountUp(() => totalUp.value, NOROUND)
const dDown = useCountUp(() => totalDown.value, NOROUND)

function memPct(s: Server) { return s.metrics ? pct(s.metrics.mem_used, s.metrics.mem_total) : 0 }
function diskPct(s: Server) { return s.metrics ? pct(s.metrics.disk_used, s.metrics.disk_total) : 0 }
function swapPct(s: Server) { return s.metrics ? pct(s.metrics.swap_used, s.metrics.swap_total) : 0 }
function lvl(v: number) { return v >= 90 ? 'crit' : v >= 70 ? 'warn' : 'ok' }
// 到期倒计时着色：不足 3 天红、不足 7 天黄、其余绿
function expiryCls(d: number) { return d < 3 ? 'crit' : d < 7 ? 'warn' : 'ok' }
function trafficReady(t: PublicTraffic) { return t.calibrated || t.sample_count >= 2 }
function trafficPct(t: PublicTraffic) { return pct(t.used_bytes || 0, t.limit_bytes || 0) }
function trafficModeLabel(mode: string) {
  return ({ sum: 'IN + OUT', max: '双向取大', rx: '仅 IN', tx: '仅 OUT' } as Record<string, string>)[mode] || 'IN + OUT'
}
function trafficIncomplete(t: PublicTraffic) {
  return !t.calibrated && t.sample_count > 0 && t.coverage_start > t.cycle_start + 3600
}
function fmtShortDate(ts: number, withTime = false) {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  const date = `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  return withTime ? `${date} ${pad(d.getHours())}:${pad(d.getMinutes())}` : date
}

// 仪表盘几何
const GAUGE_R = 26
const GAUGE_C = 2 * Math.PI * GAUGE_R
function dashOff(v: number) { return GAUGE_C * (1 - Math.min(Math.max(v, 0), 100) / 100) }
function gauges(s: Server) {
  const cpu = s.metrics!.cpu_percent
  const mp = memPct(s), dp = diskPct(s)
  return [
    { key: 'CPU', val: cpu, lvl: lvl(cpu), off: dashOff(cpu), sub: '' },
    { key: '内存', val: mp, lvl: lvl(mp), off: dashOff(mp), sub: `${fmtBytes(s.metrics!.mem_used)} / ${fmtBytes(s.metrics!.mem_total)}` },
    { key: '磁盘', val: dp, lvl: lvl(dp), off: dashOff(dp), sub: `${fmtBytes(s.metrics!.disk_used)} / ${fmtBytes(s.metrics!.disk_total)}` },
  ]
}

// 迷你趋势图路径
const SPARK_H = 34
function sparkPts(arr: number[]) {
  const n = arr.length
  if (n < 2) return [] as [number, number][]
  return arr.map((v, i): [number, number] => {
    const x = (i / (n - 1)) * 100
    const y = SPARK_H - (Math.min(Math.max(v, 0), 100) / 100) * (SPARK_H - 3) - 1.5
    return [x, y]
  })
}
function sparkLine(arr: number[]) {
  const pts = sparkPts(arr)
  if (!pts.length) return ''
  return pts.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(2)},${p[1].toFixed(2)}`).join(' ')
}
function sparkArea(arr: number[]) {
  const line = sparkLine(arr)
  if (!line) return ''
  return `${line} L100,${SPARK_H} L0,${SPARK_H} Z`
}

async function fetchData() {
  try {
    const [pub, spk] = await Promise.all([
      apiGet<{ servers: Server[] }>('/api/monitor/public'),
      apiGet<{ servers: Spark[] }>('/api/monitor/public/sparklines?range=1h').catch(() => null),
    ])
    const sparks: Record<string, Spark> = {}
    if (spk?.servers) for (const s of spk.servers) sparks[s.name] = s
    const list = Array.isArray(pub?.servers) ? [...pub.servers] : []
    // 面板本机与其它机器一样走 public_visible：关闭后公开首页和已登录首页都不显示。
    for (const s of list) s.spark = sparks[s.name] || null
    servers.value = list
  } catch {}
}

async function doRefresh() {
  if (refreshing.value) return
  refreshing.value = true
  await fetchData()
  setTimeout(() => { refreshing.value = false }, 600)
}

// --- 可用性热力图（Y=机器, X=时间桶）---
const heatEl = ref<HTMLElement | null>(null)
const heatChart = shallowRef<echarts.ECharts | null>(null)
const heatData = ref<any>(null)
const heatRange = ref('24h')
const heatRanges = [
  { label: '1h', value: '1h' }, { label: '6h', value: '6h' },
  { label: '24h', value: '24h' }, { label: '7d', value: '7d' },
]
function fmtHeatTime(ts: number, range: string): string {
  const d = new Date(ts * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  if (range === '7d' || range === '24h') return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function fmtHeatAxisTime(ts: number, range: string): string {
  const d = new Date(ts * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  if (range === '7d') return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function escapeHeatHtml(value: unknown): string {
  return String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c] || c))
}
async function loadHeatmap(range: string) {
  heatRange.value = range
  try {
    const data = await apiGet<any>(`/api/monitor/heatmap?range=${range}`)
    heatData.value = data
    await nextTick()
    renderHeatmap()
  } catch {}
}
function renderHeatmap() {
  const data = heatData.value
  if (!heatEl.value || !data) return
  const servers: any[] = data.servers || []
  if (!servers.length) { heatChart.value?.clear(); return }
  if (!heatChart.value) heatChart.value = echarts.init(heatEl.value)
  const chart = heatChart.value
  const buckets: number[] = data.buckets || []
  const matrix: number[][] = data.matrix || []
  const range = data.range || '24h'
  const pts: [number, number, number][] = []
  const hasDistinctNoData = Number(data.state_count || 0) >= 4
  for (let y = 0; y < servers.length; y++) {
    const row = matrix[y] || []
    for (let x = 0; x < buckets.length; x++) {
      const raw = row[x] ?? 3
      pts.push([x, y, !hasDistinctNoData && raw === 2 ? 3 : raw])
    }
  }
  const xLabels = buckets.map((t: number) => fmtHeatAxisTime(t, range))
  const yLabels = servers.map((s: any) => s.name)
  const cw = heatEl.value.clientWidth || 600
  const maxNameLen = Math.max(...yLabels.map((n: string) => String(n).length), 4)
  const yLabelW = Math.min(maxNameLen * 7 + 18, cw < 640 ? 92 : 150)
  const padR = 4, padT = 5, padB = 30
  const rowHeight = cw < 640 ? 18 : 22
  const gridHeight = Math.max(1, servers.length) * rowHeight
  const chartH = gridHeight + padT + padB
  const labelCount = cw < 520 ? 4 : cw < 900 ? 6 : 8
  const labelInterval = Math.max(0, Math.ceil(buckets.length / labelCount) - 1)
  const states = [
    { label: '运行正常', color: '#63a887' },
    { label: '高负载', color: '#d2a34c' },
    { label: '严重负载', color: '#c96d67' },
    { label: '离线 / 无数据', color: '#b9c2cc' },
  ]
  heatEl.value.style.height = chartH + 'px'
  chart.setOption({
    animationDuration: 560,
    animationDurationUpdate: 380,
    animationEasing: 'cubicOut',
    animationEasingUpdate: 'cubicOut',
    tooltip: {
      backgroundColor: 'rgba(255,255,255,.98)', borderColor: '#dfe4ea', borderWidth: 1,
      padding: [10, 12], textStyle: { color: '#26323f', fontSize: 12 },
      extraCssText: 'border-radius:10px;box-shadow:0 10px 30px rgba(42,55,70,.14);',
      formatter: (p: any) => {
        const [x, y, v] = p.value
        const state = states[v] || states[3]
        const time = buckets[x] ? fmtHeatTime(buckets[x], range) : ''
        return `<div style="font-weight:650;margin-bottom:4px">${escapeHeatHtml(yLabels[y])}</div><div style="color:#7b8794;margin-bottom:7px">${escapeHeatHtml(time)}</div><div style="display:flex;align-items:center;gap:7px"><i style="width:8px;height:8px;border-radius:3px;background:${state.color};display:inline-block"></i>${state.label}</div>`
      },
    },
    grid: { left: yLabelW, right: padR, top: padT, height: gridHeight },
    xAxis: { type: 'category', data: xLabels, axisLabel: { interval: labelInterval, color: '#7b8794', fontSize: 10, margin: 10, hideOverlap: true }, axisTick: { show: false }, axisLine: { show: false } },
    yAxis: { type: 'category', data: yLabels, axisLabel: { color: '#606d7b', fontSize: 11, width: yLabelW - 12, overflow: 'truncate', margin: 10 }, axisTick: { show: false }, axisLine: { show: false } },
    visualMap: { type: 'piecewise', show: false, pieces: states.map((state, value) => ({ value, color: state.color })) },
    series: [{ type: 'heatmap', data: pts, progressive: 0, itemStyle: { borderColor: 'rgba(255,255,255,.96)', borderWidth: 3, borderRadius: 5 }, emphasis: { itemStyle: { borderColor: '#fff', borderWidth: 2, shadowBlur: 10, shadowColor: 'rgba(42,55,70,.18)' } } }],
  }, true)
  chart.resize()
}
function onWinResize() { if (heatData.value) renderHeatmap() }

onMounted(async () => {
  loading.value = true
  tickClock()
  clockTimer = setInterval(tickClock, 1000)
  await fetchData()
  loading.value = false
  timer = setInterval(fetchData, 30000)
  loadHeatmap('24h')
  if (auth.isAdmin) void loadUpstreamBalances()
  upstreamTimer = setInterval(() => {
    if (document.visibilityState === 'visible' && auth.isAdmin && upstreamBalanceVisible.value) void refreshUpstreamBalances()
  }, 15 * 60 * 1000)
  window.addEventListener('resize', onWinResize)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (clockTimer) clearInterval(clockTimer)
  if (upstreamTimer) clearInterval(upstreamTimer)
  window.removeEventListener('resize', onWinResize)
  heatChart.value?.dispose()
})
</script>

<style scoped>
.monitor-page { min-height: 100vh; background: transparent; }
.monitor-content { padding: 22px 24px 56px; max-width: 1320px; margin: 0 auto; }

/* ===== 顶部标题 ===== */
.hero { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; margin-bottom: 22px; flex-wrap: wrap; animation: heroIn .7s var(--ease-emphasized) both; }
@keyframes heroIn { from { opacity: 0; transform: translateY(7px); } }
.hero-title {
  font-size: 25px; font-weight: 700; letter-spacing: -.025em; margin: 0; line-height: 1.1; color: var(--text);
}
.hero-sub { margin: 4px 0 0; font-size: 13px; color: var(--text-3); }
.hero-right { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.clock {
  display: inline-flex; align-items: center; gap: 6px; font-variant-numeric: tabular-nums;
  font-size: 13px; font-weight: 650; color: var(--text-2); letter-spacing: .02em;
  padding: 6px 12px; background: rgba(255,255,255,.72); border: 1px solid var(--border); border-radius: 9px; box-shadow: var(--shadow-sm); backdrop-filter: blur(10px);
}
.clock svg { opacity: .55; }
.auto-badge { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--text-3); font-weight: 500; }
.auto-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 0 0 rgba(23,105,165,.35); }
.auto-dot.active { animation: ping 1s ease-out; }
@keyframes ping { 0% { box-shadow: 0 0 0 0 rgba(23,105,165,.38); } 100% { box-shadow: 0 0 0 9px rgba(23,105,165,0); } }
.refresh-btn {
  display: inline-grid; place-items: center; width: 32px; height: 32px; border-radius: 9px;
  border: 1px solid var(--border); background: rgba(255,255,255,.72); color: var(--text-2); cursor: pointer; transition: color .24s var(--ease-standard), background .24s var(--ease-standard), box-shadow .3s var(--ease-standard), transform .3s var(--ease-emphasized);
}
.refresh-btn:hover:not(:disabled) { color: var(--accent-strong); border-color: rgba(23,105,165,.22); background: #fff; box-shadow: var(--shadow-sm); transform: translateY(-1px); }
.refresh-btn:disabled { opacity: .5; cursor: not-allowed; }
.refresh-btn.spinning svg { animation: spin .8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

/* ===== 汇总卡片 ===== */
.summary-grid { display: grid; grid-template-columns: repeat(6, 1fr); gap: 12px; margin-bottom: 22px; }
.summary-card {
  position: relative; overflow: hidden; display: flex; align-items: center; gap: 12px; padding: 15px 16px;
  background: var(--card); border: 1px solid var(--border); border-radius: 14px; backdrop-filter: blur(14px);
  box-shadow: var(--shadow-sm); transition: box-shadow .34s var(--ease-standard), transform .4s var(--ease-emphasized), border-color .28s var(--ease-standard); min-width: 0;
  animation: summaryIn .68s var(--ease-emphasized) both;
}
.summary-card:nth-child(2) { animation-delay: 45ms; } .summary-card:nth-child(3) { animation-delay: 90ms; }
.summary-card:nth-child(4) { animation-delay: 135ms; } .summary-card:nth-child(5) { animation-delay: 180ms; } .summary-card:nth-child(6) { animation-delay: 225ms; }
@keyframes summaryIn { from { opacity: 0; transform: translateY(9px) scale(.985); filter: blur(3px); } to { opacity: 1; transform: none; filter: none; } }
.summary-card::before { content: ''; position: absolute; inset: 0 0 auto; height: 1px; background: linear-gradient(90deg, transparent, rgba(255,255,255,.95), transparent); pointer-events: none; }
.summary-card:hover { box-shadow: var(--shadow); transform: translateY(-2px); border-color: var(--border-strong); }
.summary-icon { width: 42px; height: 42px; border-radius: 12px; display: grid; place-items: center; flex-shrink: 0; border: 1px solid rgba(39,95,137,.08); box-shadow: inset 0 1px 0 rgba(255,255,255,.8); }
.summary-icon.i-server { background: linear-gradient(145deg, #e8f3f9, #deedf6); color: #286c98; }
.summary-icon.i-cpu { background: linear-gradient(145deg, #eaf4fa, #e1eff7); color: #347ba8; }
.summary-icon.i-mem { background: linear-gradient(145deg, #edf1f9, #e4eaf5); color: #526c9a; }
.summary-icon.i-disk { background: linear-gradient(145deg, #edf4f6, #e4eef1); color: #527686; }
.summary-icon.i-up { background: linear-gradient(145deg, #eaf5f3, #e0efec); color: #39796e; }
.summary-icon.i-down { background: linear-gradient(145deg, #e8f2f9, #dfeef7); color: #316f9b; }
.summary-body { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.summary-val { font-size: 23px; font-weight: 770; letter-spacing: -.03em; color: var(--text); font-variant-numeric: tabular-nums; line-height: 1.05; }
.summary-val.small { font-size: 18px; }
.summary-val i { font-size: 13px; font-weight: 600; color: var(--text-3); font-style: normal; margin-left: 1px; }
.summary-label { font-size: 11.5px; color: var(--text-3); font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.summary-label b { font-weight: 650; }
.ok-text { color: var(--success); } .crit-text { color: var(--danger); }
.mini-bar { height: 4px; border-radius: 3px; background: var(--bg); overflow: hidden; margin-top: 5px; }
.mini-fill { height: 100%; border-radius: 3px; transition: width 1s var(--ease-emphasized); }
.mini-fill.ok { background: var(--success); } .mini-fill.warn { background: var(--warn); } .mini-fill.crit { background: var(--danger); }

/* ===== 分节栏 ===== */
.section-bar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; gap: 12px; flex-wrap: wrap; }
.section-title { font-size: 14px; font-weight: 680; color: var(--text); }
.heat-range { display: flex; align-items: center; gap: 0; flex-wrap: wrap; padding:3px; border:1px solid rgba(28,48,70,.09); border-radius:8px; background:#eef1f4; }
.heat-range-btn {
  padding: 4px 11px; border-radius: 5px; border: 0;
  background: transparent; color: var(--text-3); font-size: 12px; font-weight: 600; cursor: pointer; transition: background .18s var(--ease-standard), color .18s var(--ease-standard), box-shadow .18s var(--ease-standard); font-family: inherit;
}
.heat-range-btn:hover { color: var(--text); }
.heat-range-btn.active { background: #fff; color: var(--text); box-shadow: 0 1px 2px rgba(30,45,60,.1); }
.heat-range-btn:focus-visible { outline:0; box-shadow:inset 0 0 0 2px rgba(29,39,51,.16); }
.heat-legend { display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--text-3); margin-left: 10px; }
.hm-dot { width: 8px; height: 8px; border-radius: 3px; display: inline-block; margin-left: 6px; box-shadow: inset 0 0 0 1px rgba(31,43,55,.05); }
.hm-dot.ok { background: #63a887; } .hm-dot.warn { background: #d2a34c; } .hm-dot.crit { background: #c96d67; } .hm-dot.none { background: #b9c2cc; }

.heatmap-card { background: var(--card); border: 1px solid var(--border); border-radius: 14px; box-shadow: var(--shadow-sm); backdrop-filter: blur(14px); padding: 14px 16px 10px; margin-bottom: 26px; animation: panelIn .72s .16s var(--ease-emphasized) both; }
@keyframes panelIn { from { opacity: 0; transform: translateY(8px); } }
.heat-chart { width: 100%; height: 58px; min-height: 0; }
.heat-chart:empty { display: none; }
.heat-empty { text-align: center; color: var(--text-3); padding: 24px; font-size: 13px; }

/* ===== 服务器卡片 ===== */
.server-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 16px; }

.server-card {
  position: relative; overflow: hidden;
  background: var(--card); border: 1px solid var(--border); border-radius: 16px;
  box-shadow: var(--shadow-sm); backdrop-filter: blur(14px); transition: box-shadow .34s var(--ease-standard), transform .42s var(--ease-emphasized), border-color .28s var(--ease-standard);
  animation: cardIn .7s var(--ease-emphasized) backwards; animation-delay: calc(var(--i) * 55ms);
}
@keyframes cardIn { from { opacity: 0; transform: translateY(11px) scale(.988); filter: blur(3px); } to { opacity: 1; transform: none; filter: none; } }
.server-card:hover { box-shadow: var(--shadow); transform: translateY(-2px); border-color: var(--border-strong); }
.upstream-balance-card:hover { transform: translateY(-2px); }

.card-top-line { height: 3px; }
.card-top-line.online { background: linear-gradient(90deg, #5c7c63, #8fb097); }
.card-top-line.offline { background: linear-gradient(90deg, #c2685c, #d98a7f); }

.card-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; padding: 14px 16px 8px; }
.card-title { display: flex; align-items: flex-start; gap: 8px; min-width: 0; flex: 1; }
.status-beacon { width: 9px; height: 9px; border-radius: 50%; flex-shrink: 0; margin-top: 5px; }
.status-beacon.online { background: #5c9c6e; box-shadow: 0 0 8px rgba(92,156,110,.55); animation: beacon 2.4s ease-in-out infinite; }
.status-beacon.offline { background: #c2685c; box-shadow: 0 0 6px rgba(194,104,92,.35); }
@keyframes beacon { 0%,100% { opacity: 1; } 50% { opacity: .35; } }
.server-name {
  font-weight: 680; font-size: 14.5px; line-height: 1.35; min-width: 0; color: var(--text);
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; word-break: break-word;
}
.status-badge {
  display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0; white-space: nowrap;
  padding: 3px 9px; border-radius: 20px; font-size: 11px; font-weight: 650;
}
.status-badge .badge-dot { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.status-badge.online { background: var(--success-soft); color: var(--success); }
.status-badge.offline { background: var(--danger-soft); color: var(--danger); }

.tag-line { display: flex; flex-wrap: wrap; gap: 6px; padding: 0 16px 12px; }
.tag { padding: 2px 8px; border-radius: 6px; font-size: 11px; font-weight: 500; background: var(--bg-soft); color: var(--text-2); white-space: nowrap; }
.tag.loc { background: var(--accent-soft); color: var(--accent-strong); }
.tag.spec { font-variant-numeric: tabular-nums; }
.tag.price { background: #f5efe0; color: #946f24; font-variant-numeric: tabular-nums; }
.tag.expiry { display: inline-flex; align-items: center; gap: 5px; font-variant-numeric: tabular-nums; }
.tag.expiry .exp-dot { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.tag.expiry.ok { background: var(--success-soft); color: var(--success); }
.tag.expiry.warn { background: #f6edd6; color: #a97e1f; }
.tag.expiry.crit { background: var(--danger-soft); color: var(--danger); }

/* 仪表盘 */
.gauges { display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px; padding: 2px 12px 12px; }
.gauge { position: relative; display: flex; flex-direction: column; align-items: center; text-align: center; }
.gauge-svg { width: 100%; max-width: 76px; aspect-ratio: 1; transform: rotate(-90deg); }
.gauge-bg { fill: none; stroke: var(--bg); stroke-width: 5; }
.gauge-fg { fill: none; stroke-width: 5; stroke-linecap: round; transition: stroke-dashoffset 1.15s var(--ease-emphasized); }
.gauge-center { position: absolute; top: 0; left: 0; right: 0; display: grid; place-items: center; pointer-events: none; }
.gauge-svg + .gauge-center { height: 100%; max-height: 76px; }
.gauge-center { height: min(76px, 100%); }
.gauge-val { font-size: 15px; font-weight: 750; color: var(--text); font-variant-numeric: tabular-nums; line-height: 1; }
.gauge-val i { font-size: 9px; font-weight: 600; font-style: normal; color: var(--text-3); margin-left: 1px; }
.gauge-val.warn { color: var(--warn); } .gauge-val.crit { color: var(--danger); }
.gauge-meta { display: flex; flex-direction: column; gap: 1px; margin-top: 3px; width: 100%; }
.gauge-label { font-size: 11px; font-weight: 650; color: var(--text-2); }
.gauge-sub { font-size: 9.5px; color: var(--text-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }

/* 迷你趋势图 */
.spark-wrap { position: relative; margin: 0 16px 12px; height: 40px; border-radius: 9px; background: var(--bg-soft); overflow: hidden; }
.spark { width: 100%; height: 100%; display: block; }
.spark-area { fill: url(#spark-grad); }
.spark-line { fill: none; stroke: var(--accent); stroke-width: 1.6; stroke-linejoin: round; stroke-linecap: round; vector-effect: non-scaling-stroke; }
.spark-tag { position: absolute; top: 5px; left: 8px; font-size: 9.5px; color: var(--text-3); font-weight: 600; letter-spacing: .02em; }

/* 交换分区 */
.swap-row { display: flex; align-items: center; gap: 8px; padding: 0 16px 12px; font-size: 11px; }
.swap-label { color: var(--text-3); font-weight: 600; flex-shrink: 0; }
.swap-track { flex: 1; height: 5px; border-radius: 3px; background: var(--bg); overflow: hidden; }
.swap-fill { height: 100%; border-radius: 3px; transition: width 1s var(--ease-emphasized); }
.swap-fill.ok { background: var(--success); } .swap-fill.warn { background: var(--warn); } .swap-fill.crit { background: var(--danger); }
.swap-detail { color: var(--text-3); flex-shrink: 0; font-variant-numeric: tabular-nums; }

/* 详细信息网格 */
.info-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 7px; padding: 4px 16px 14px; border-top: 1px solid var(--border); margin: 0 0 0; }
.info-cell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.info-cell svg { width: 15px; height: 15px; flex-shrink: 0; color: var(--text-3); opacity: .85; }
.info-cell > div { display: flex; flex-direction: column; min-width: 0; line-height: 1.25; }
.info-cell.wide { grid-column: span 2; }
.ic-label { font-size: 10px; color: var(--text-3); font-weight: 500; }
.ic-val { font-size: 12px; color: var(--text); font-weight: 600; font-variant-numeric: tabular-nums; }
.ic-val.ellip { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.card-no-data { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 30px 16px; color: var(--text-3); font-size: 13px; }

.public-traffic { padding: 11px 16px 12px; border-top: 1px solid var(--border); }
.public-traffic-head { display: flex; justify-content: space-between; gap: 12px; font-size: 11px; color: var(--text-3); }
.public-traffic-head b { color: var(--text); font-size: 12px; font-weight: 680; font-variant-numeric: tabular-nums; text-align: right; }
.public-traffic-track { height: 5px; margin: 7px 0 6px; overflow: hidden; border-radius: 4px; background: var(--bg); }
.public-traffic-track i { display: block; height: 100%; border-radius: inherit; transition: width 1s var(--ease-emphasized); }
.public-traffic-track i.ok { background: var(--success); } .public-traffic-track i.warn { background: var(--warn); } .public-traffic-track i.crit { background: var(--danger); }
.public-traffic-meta { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 10px; font-size: 10px; color: var(--text-3); }
.warn-text { color: var(--warn); }

/* 管理员专属上游余额：两个官方数据源合并在一张卡片里，可拖动子卡片调整顺序。 */
.upstream-balance-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 9px; padding: 0 12px 12px; }
.upstream-balance-item {
  min-width: 0; padding: 11px 12px; border: 1px solid var(--border); border-radius: 10px; background: var(--bg-soft);
  cursor: grab; transition: border-color .2s var(--ease-standard), box-shadow .2s var(--ease-standard), opacity .2s ease, transform .2s ease;
}
.upstream-balance-item:active { cursor: grabbing; }
.upstream-balance-item:hover { border-color: var(--border-strong); box-shadow: var(--shadow-sm); }
.upstream-balance-item.dragging { opacity: .45; transform: scale(.98); }
.upstream-balance-item.drag-over { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.upstream-balance-head { display: flex; align-items: center; gap: 7px; min-width: 0; }
.upstream-provider-mark { display: inline-grid; place-items: center; width: 28px; height: 22px; border-radius: 6px; font-size: 9px; font-weight: 750; letter-spacing: .02em; flex-shrink: 0; }
.upstream-provider-mark.oci { background: #f7ead6; color: #9a6e23; }
.upstream-provider-mark.cloudflare { background: #e7eff7; color: #286c98; }
.upstream-provider-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; font-weight: 650; color: var(--text-2); }
.upstream-drag-hint { margin-left: auto; color: var(--text-3); font-size: 14px; line-height: 1; letter-spacing: -3px; opacity: .7; }
.upstream-balance-value { margin-top: 10px; font-size: 20px; line-height: 1.15; font-weight: 750; color: var(--text); font-variant-numeric: tabular-nums; letter-spacing: -.02em; }
.upstream-balance-value.muted { color: var(--text-2); font-size: 18px; }
.upstream-balance-meta { min-height: 30px; margin-top: 4px; color: var(--text-3); font-size: 10.5px; line-height: 1.45; font-variant-numeric: tabular-nums; }
.upstream-balance-track { height: 4px; margin-top: 7px; overflow: hidden; border-radius: 4px; background: var(--bg); }
.upstream-balance-track i { display: block; height: 100%; border-radius: inherit; transition: width .5s var(--ease-emphasized); }
.upstream-balance-track i.ok { background: var(--success); } .upstream-balance-track i.warn { background: var(--warn); } .upstream-balance-track i.crit { background: var(--danger); }
.upstream-balance-foot { margin-top: 7px; overflow: hidden; color: var(--text-3); font-size: 9.5px; line-height: 1.35; text-overflow: ellipsis; white-space: nowrap; }
.upstream-card-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.upstream-refresh-link { padding: 0; border: 0; background: transparent; color: var(--accent-strong); font: inherit; font-size: 11px; cursor: pointer; }
.upstream-refresh-link:disabled { cursor: wait; opacity: .5; }

.card-footer { padding: 9px 16px; border-top: 1px solid var(--border); }
.footer-time { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--text-3); }
.footer-time .dot { width: 6px; height: 6px; border-radius: 50%; }
.footer-time .dot.online { background: #5c9c6e; } .footer-time .dot.offline { background: #c2685c; }

/* ===== 响应式 ===== */
@media (max-width: 1080px) { .summary-grid { grid-template-columns: repeat(3, 1fr); } }
@media (max-width: 760px) {
  .monitor-content { padding: 16px 13px 40px; }
  .hero-title { font-size: 20px; }
  .summary-grid { grid-template-columns: repeat(2, 1fr); gap: 10px; }
  .server-grid { grid-template-columns: 1fr; }
  .upstream-balance-grid { grid-template-columns: 1fr 1fr; }
  .heat-legend { display: none; }
}
@media (max-width: 380px) { .summary-grid { grid-template-columns: 1fr 1fr; } .summary-icon { width: 38px; height: 38px; } .summary-val { font-size: 20px; } }
</style>
