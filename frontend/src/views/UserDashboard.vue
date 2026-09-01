<template>
  <div>
    <!-- 页面头 -->
    <div class="dash-head">
      <div>
        <h2 class="page-title">控制台</h2>
        <p class="page-sub">{{ greeting }}，{{ auth.user?.username }}，这里是你的服务概览</p>
      </div>
      <div class="dash-actions">
        <n-button size="small" quaternary :loading="refreshing" @click="reload">
          <template #icon><n-icon><RefreshOutline /></n-icon></template>
          刷新
        </n-button>
        <n-button size="small" secondary @click="router.push('/sub')">
          <template #icon><n-icon><LinkOutline /></n-icon></template>
          订阅管理
        </n-button>
        <n-button size="small" type="primary" @click="router.push('/shop')">
          <template #icon><n-icon><CartOutline /></n-icon></template>
          去商城
        </n-button>
      </div>
    </div>

    <!-- 状态提醒：按套餐维度判定，不再拿单一 expiry_at 代表整个账号 -->
    <transition-group name="alert" tag="div">
      <n-alert v-for="a in alerts" :key="a.key" :type="a.type" class="dash-alert">
        {{ a.text }}
        <router-link v-if="a.to" :to="a.to">{{ a.action }}</router-link>
      </n-alert>
    </transition-group>

    <div v-if="!activeCount" class="onboarding-strip">
      <div class="onboarding-copy"><b>从这里开始</b><span>当前没有生效中的套餐，完成下面两步即可使用服务。</span></div>
      <button type="button" @click="router.push('/shop')"><i>1</i><span><b>选择套餐</b><small>对比流量、时长与价格</small></span></button>
      <button type="button" @click="router.push('/sub')"><i>2</i><span><b>导入订阅</b><small>复制地址或一键打开客户端</small></span></button>
      <button type="button" @click="showHelp"><i>?</i><span><b>查看帮助</b><small>安装、连接与常见问题</small></span></button>
    </div>

    <!-- 核心指标 -->
    <div class="kpi-row">
      <StatCard
        label="剩余流量" :value="remainingText" :badge="usedBadge" :badge-color="ringColor"
        :sub="trafficSub" :delay="0"
      >
        <div class="mini-progress">
          <div class="mini-fill" :style="{ width: (metered ? usedPct : 100) + '%', background: ringColor }" />
        </div>
      </StatCard>

      <StatCard
        label="生效中套餐" :value="activeCount + ' 份'" :sub="planSub"
        :badge="queuedCount ? '排队 ' + queuedCount : ''" badge-color="#5e7a99"
        clickable :delay="60" @click="router.push('/sub')"
      />

      <StatCard
        label="积分" :value="String(dPoints)" :sub="yuan(dash.points || 0) + ' · 去商城兑换'"
        clickable :delay="120" @click="router.push('/shop')"
      />

      <StatCard
        :label="rangeLabel + '用量'" :value="fmtBytes(trendTotal)"
        :sub="'日均 ' + fmtBytes(trendAvg)" :delay="180"
      />
    </div>

    <n-card v-if="notices.length" size="small" class="sec" style="margin-bottom:16px;">
      <template #header>
        <span class="sec-title">最新公告</span>
        <router-link class="sec-link" to="/notices">查看全部</router-link>
      </template>
      <n-list bordered size="small">
        <n-list-item v-for="n in notices.slice(0,3)" :key="n.id" class="notice-row" @click="openNotice(n)">
          <n-thing>
            <template #header><span style="font-weight:600;">{{ n.title }}</span><n-tag v-if="n.pinned" size="tiny" type="warning" style="margin-left:6px;">置顶</n-tag></template>
            <template #header-extra><span style="font-size:11px;color:var(--text-3);">{{ fmtDate(n.created_at) }}</span></template>
          </n-thing>
        </n-list-item>
      </n-list>
    </n-card>

    <!-- 主区域：左侧用量环，右侧流量趋势 -->
    <div class="dash-grid">
      <n-card size="small" class="sec usage-card" style="margin-bottom:0;">
        <template #header><span class="sec-title">流量用量</span></template>
        <div class="ring-wrap">
          <div class="ring-box">
            <svg viewBox="0 0 140 140" class="ring-svg">
              <circle cx="70" cy="70" r="58" fill="none" stroke="var(--bg-soft)" stroke-width="12" />
              <circle
                cx="70" cy="70" r="58" fill="none" :stroke="ringColor" stroke-width="12" stroke-linecap="round"
                :stroke-dasharray="CIRC" :stroke-dashoffset="ringOffset" class="ring-arc"
              />
            </svg>
            <div class="ring-center">
              <template v-if="metered">
                <span class="ring-pct">{{ ringPctText }}<i>%</i></span>
                <span class="ring-label">已使用</span>
              </template>
              <template v-else>
                <span class="ring-pct ring-inf">—</span>
                <span class="ring-label">暂无额度</span>
              </template>
            </div>
          </div>
          <div class="ring-foot">{{ ringFoot }}</div>
        </div>
        <n-space vertical size="small" style="margin-top:14px;">
          <n-button block secondary @click="router.push('/sub')">
            <template #icon><n-icon><LinkOutline /></n-icon></template>
            管理订阅
          </n-button>
          <n-button block secondary @click="router.push('/orders')">
            <template #icon><n-icon><ReceiptOutline /></n-icon></template>
            订单记录
          </n-button>
        </n-space>
      </n-card>

      <div class="dash-main">
        <n-card size="small" class="sec">
          <template #header>
            <span class="sec-title">流量趋势</span>
            <n-radio-group v-model:value="trendRange" size="small" :disabled="trendLoading">
              <n-radio-button value="7d">7天</n-radio-button>
              <n-radio-button value="30d">30天</n-radio-button>
            </n-radio-group>
          </template>
          <n-spin :show="trendLoading">
            <TrafficTrendChart v-if="trendData.length" :data="trendData" />
            <div v-else class="empty">暂无流量数据</div>
          </n-spin>
          <div v-if="trendData.length" class="trend-foot">
            <span class="tf-item"><i class="dot up" />上行 <b>{{ fmtBytes(trendUp) }}</b></span>
            <span class="tf-item"><i class="dot down" />下行 <b>{{ fmtBytes(trendDown) }}</b></span>
            <span class="tf-item tf-peak" v-if="peakDay">峰值 {{ peakDay.date }} · <b>{{ fmtBytes(peakDay.total) }}</b></span>
          </div>
        </n-card>
      </div>
    </div>

    <n-modal v-model:show="showNotice" preset="card" style="max-width:640px;" :title="activeNotice?.title">
      <template #header-extra v-if="activeNotice">
        <span style="font-size:12px;color:var(--text-3);">{{ fmtDate(activeNotice.created_at) }}</span>
      </template>
      <div class="md" v-html="mdToHtml(activeNotice?.content || '')" />
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NCard, NAlert, NButton, NList, NListItem, NThing, NTag, NRadioGroup, NRadioButton, NModal, NSpace, NSpin, NIcon } from 'naive-ui'
import { LinkOutline, CartOutline, ReceiptOutline, RefreshOutline } from '@vicons/ionicons5'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { apiGet, apiList } from '@/api'
import { fmtBytes, fmtDate, daysLeft, yuan, pct } from '@/utils/format'
import { mdToHtml } from '@/utils/markdown'
import { useCountUp } from '@/utils/countup'
import StatCard from '@/components/StatCard.vue'
import TrafficTrendChart from '@/components/TrafficTrendChart.vue'
import { openHelp } from '@/utils/help'

const router = useRouter(); const auth = useAuthStore(); const config = useConfigStore()
function showHelp() { openHelp(config.config, router) }
const dash = ref<any>({}); const notices = ref<any[]>([])
const trendRange = ref('7d'); const trendData = ref<any[]>([]); const trendLoading = ref(false)
const refreshing = ref(false)
const showNotice = ref(false); const activeNotice = ref<any>(null)
function openNotice(n: any) { activeNotice.value = n; showNotice.value = true }

const greeting = computed(() => {
  const h = new Date().getHours()
  return h < 6 ? '夜深了' : h < 12 ? '早上好' : h < 14 ? '中午好' : h < 18 ? '下午好' : '晚上好'
})

// ---- 流量口径 ----
// 所有流量额度都是有限数字；total=0 就是没有额度。
const traffic = computed(() => dash.value.traffic || {})
// metered = 存在可以算百分比的额度。没有它，环形图和进度条都无意义。
const metered = computed(() => (traffic.value.total || 0) > 0)
const usedPct = computed(() => pct(traffic.value.used, traffic.value.total))
const usedBadge = computed(() => metered.value && usedPct.value > 0 ? '已用 ' + usedPct.value + '%' : '')
const ringColor = computed(() => {
  if (!metered.value) return '#b8b8b8'
  return usedPct.value > 90 ? '#c2685c' : usedPct.value > 70 ? '#bf9540' : '#6f8f76'
})

const remainingText = computed(() => {
  if (!dash.value.traffic) return '—'
  if (metered.value) return fmtBytes(traffic.value.remaining)
  return '无额度'
})
const trafficSub = computed(() => {
  const t = traffic.value
  if (!dash.value.traffic) return ''
  const parts: string[] = []
  if (metered.value) parts.push(`已用 ${fmtBytes(t.used)} / ${fmtBytes(t.total)}`)
  return parts.join(' · ') || '还没有可用套餐'
})

// 环形：dashoffset 过渡比改 dasharray 更平滑（后者会连虚线间隔一起跳）
const CIRC = 2 * Math.PI * 58
const dRingPct = useCountUp(() => (metered.value ? usedPct.value : 0), { round: false })
const ringPctText = computed(() => {
  const v = dRingPct.value
  return v >= 100 || Math.abs(v - Math.round(v)) < 0.05 ? String(Math.round(v)) : v.toFixed(1)
})
const ringOffset = computed(() => {
  if (!metered.value) return CIRC
  return CIRC * (1 - Math.min(usedPct.value, 100) / 100)
})
const ringFoot = computed(() => {
  if (!dash.value.traffic) return '—'
  if (metered.value) return `${fmtBytes(traffic.value.used)} / ${fmtBytes(traffic.value.total)}`
  return '去商城选购套餐'
})

// ---- 套餐（只报汇总，不在控制台铺明细：多份并存时任何单份都代表不了账号）----
const plans = computed<any[]>(() => dash.value.plans || [])
const activePlans = computed(() => plans.value.filter(p => p.status === 'active'))
const activeCount = computed(() => activePlans.value.length)
const queuedCount = computed(() => plans.value.filter(p => p.status === 'queued').length)
// 最近到期的那一份，作为「什么时候需要续」的提示；不过期的份不参与比较
const nextExpiry = computed<number | null>(() => {
  const ts = activePlans.value.map(p => p.expiry_at).filter((t: number) => t > 0)
  return ts.length ? Math.min(...ts) : null
})
const nextExpiryDays = computed(() => daysLeft(nextExpiry.value))
const planSub = computed(() => {
  if (!plans.value.length) return '还没有套餐，去商城看看'
  if (!activeCount.value) return '全部已到期或用尽'
  if (nextExpiry.value === null) return activeCount.value > 1 ? '均不过期' : '不过期'
  const prefix = activeCount.value > 1 ? '最近 ' : ''
  return `${prefix}${fmtDate(nextExpiry.value)} 到期（剩 ${nextExpiryDays.value} 天）`
})

const dPoints = useCountUp(() => dash.value.points || 0)

// ---- 提醒 ----
// 判定全部走套餐维度。旧版用 users.expiry_at 这一个指针，它在多份并存时既可能
// 早于真正在用的那份（误报「账号已过期」），也可能停在被顺延前的值。
const alerts = computed(() => {
  const out: { key: string; type: 'error' | 'warning' | 'info'; text: string; to?: string; action?: string }[] = []
  if (auth.user?.status === 'banned') {
    out.push({ key: 'banned', type: 'error', text: '账号已被封禁，请联系管理员' })
    return out
  }
  if (plans.value.length && !activeCount.value) {
    out.push({
      key: 'no-active', type: 'warning', text: '套餐已全部到期或用尽，',
      to: '/shop', action: '去续费',
    })
  } else if (nextExpiryDays.value !== null && nextExpiryDays.value <= 7) {
    const many = activeCount.value > 1
    out.push({
      key: 'expiring', type: 'warning',
      text: `${many ? '最近一份套餐' : '套餐'}将在 ${Math.max(nextExpiryDays.value, 0)} 天后到期，`,
      to: '/shop', action: '去续费',
    })
  }
  if (metered.value && (traffic.value.used || 0) >= traffic.value.total) {
    out.push({ key: 'exhausted', type: 'warning', text: '流量已用尽，', to: '/shop', action: '购买流量包' })
  }
  return out
})

// ---- 趋势 ----
const rangeLabel = computed(() => (trendRange.value === '30d' ? '近 30 天' : '近 7 天'))
const trendUp = computed(() => trendData.value.reduce((s, d) => s + (d.up || 0), 0))
const trendDown = computed(() => trendData.value.reduce((s, d) => s + (d.down || 0), 0))
const trendTotal = computed(() => trendUp.value + trendDown.value)
const trendAvg = computed(() => (trendData.value.length ? trendTotal.value / trendData.value.length : 0))
const peakDay = computed(() => {
  let best: { date: string; total: number } | null = null
  for (const d of trendData.value) {
    const total = (d.up || 0) + (d.down || 0)
    if (total > 0 && (!best || total > best.total)) best = { date: (d.date || '').slice(5), total }
  }
  return best
})

async function loadTrend() {
  trendLoading.value = true
  try { trendData.value = await apiList(`/api/user/stats/traffic?range=${trendRange.value}`) } catch {}
  finally { trendLoading.value = false }
}
watch(trendRange, loadTrend)

async function loadDash() {
  try { dash.value = await apiGet('/api/user/dashboard') || {} } catch {}
}

async function reload() {
  refreshing.value = true
  try { await Promise.all([loadDash(), loadTrend()]) } finally { refreshing.value = false }
}

onMounted(async () => {
  await loadDash()
  try { notices.value = await apiList('/api/user/announcements') } catch {}
  await loadTrend()
})
</script>

<style scoped>
.page-title{font-size:21px;margin-bottom:4px}
.page-sub{color:var(--text-2);margin-bottom:0}
a{color:var(--accent-strong)}
.dash-head{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;flex-wrap:wrap;margin-bottom:20px}
.dash-actions{display:flex;gap:8px;flex-shrink:0}
.sec-title{font-weight:650;font-size:14px}
.sec-link{font-size:12px;font-weight:400;margin-left:10px}

/* 提醒 */
.dash-alert{margin-bottom:10px}
.alert-enter-active,.alert-leave-active{transition:opacity .25s ease,transform .25s ease}
.alert-enter-from,.alert-leave-to{opacity:0;transform:translateY(-6px)}
.onboarding-strip{display:grid;grid-template-columns:minmax(220px,1.25fr) repeat(3,minmax(160px,1fr));gap:8px;margin:0 0 16px;padding:10px;border:1px solid var(--border);border-radius:14px;background:color-mix(in srgb,var(--card) 86%,var(--bg-soft));box-shadow:var(--shadow-sm)}
.onboarding-copy{display:flex;flex-direction:column;justify-content:center;padding:4px 8px}.onboarding-copy b{font-size:13px}.onboarding-copy span{margin-top:2px;color:var(--text-3);font-size:11.5px;line-height:1.5}
.onboarding-strip button{display:flex;align-items:center;gap:9px;padding:8px 9px;border:0;border-radius:10px;background:var(--card);color:inherit;text-align:left;font:inherit;cursor:pointer;transition:transform .2s var(--ease-emphasized),box-shadow .2s ease}.onboarding-strip button:hover{transform:translateY(-2px);box-shadow:var(--shadow-sm)}
.onboarding-strip button i{display:grid;place-items:center;flex:none;width:26px;height:26px;border-radius:8px;background:#e8ecef;color:#4f5b65;font-size:11px;font-style:normal;font-weight:700}.onboarding-strip button span{display:flex;min-width:0;flex-direction:column}.onboarding-strip button b{font-size:12px}.onboarding-strip button small{overflow:hidden;color:var(--text-3);font-size:10.5px;white-space:nowrap;text-overflow:ellipsis}

/* KPI */
.kpi-row{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:12px;margin-bottom:16px}
.mini-progress{height:4px;border-radius:2px;background:var(--bg-soft);overflow:hidden}
.mini-fill{height:100%;border-radius:2px;transition:width .6s cubic-bezier(.22,1,.36,1),background .4s ease}

/* 公告 */
.notice-row{cursor:pointer;transition:background .16s}
.notice-row:hover{background:var(--bg-soft)}

/* 主区域两栏 */
.dash-grid{display:grid;grid-template-columns:minmax(220px,260px) 1fr;gap:16px;align-items:start}
@media (max-width:840px){.dash-grid{grid-template-columns:1fr}}
@media (max-width:980px){.onboarding-strip{grid-template-columns:1fr}.onboarding-copy{padding-bottom:8px}}
.dash-main{display:flex;flex-direction:column;gap:16px;min-width:0}
.usage-card :deep(.n-card__content){padding-bottom:10px}

/* 环形 */
.ring-wrap{display:flex;flex-direction:column;align-items:center;padding:4px 0 2px}
.ring-box{position:relative;width:150px;height:150px}
.ring-svg{width:100%;height:100%;transform:rotate(-90deg)}
.ring-arc{transition:stroke-dashoffset .8s cubic-bezier(.22,1,.36,1),stroke .4s ease}
.ring-center{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center}
.ring-pct{font-size:26px;font-weight:750;letter-spacing:-0.02em;line-height:1;font-variant-numeric:tabular-nums}
.ring-pct i{font-style:normal;font-size:15px;font-weight:650;margin-left:1px}
.ring-inf{font-size:30px}
.ring-label{font-size:11px;color:var(--text-3);margin-top:4px}
.ring-foot{font-size:12px;color:var(--text-2);margin-top:10px;text-align:center}
.usage-card :deep(.n-space){gap:6px!important}

/* 趋势页脚汇总 */
.trend-foot{display:flex;flex-wrap:wrap;gap:16px;margin-top:10px;padding-top:10px;border-top:1px solid var(--border);font-size:12px;color:var(--text-2)}
.tf-item{display:inline-flex;align-items:center;gap:5px}
.tf-item b{color:var(--text);font-weight:650;font-variant-numeric:tabular-nums}
.tf-peak{margin-left:auto;color:var(--text-3)}
.dot{width:8px;height:8px;border-radius:2px;display:inline-block}
.dot.up{background:#6f8f76}
.dot.down{background:#5e7a99}

.empty{text-align:center;color:var(--text-3);padding:40px 0;font-size:13px}

@media (prefers-reduced-motion: reduce){
  .ring-arc,.mini-fill{transition:none}
}
</style>
