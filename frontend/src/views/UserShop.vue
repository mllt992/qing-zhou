<template>
  <div>
    <div class="shop-head">
      <div>
        <h2 class="page-title">积分商城</h2>
        <p class="page-sub">套餐、流量与时长逐项对比，购买前明确知道所得内容</p>
      </div>
      <div class="balance-pill"><small>可用积分</small><b>{{ auth.user?.points || 0 }}</b><span>{{ yuan(auth.user?.points || 0) }}</span></div>
    </div>
    <div class="shop-summary">
      <span><b>{{ packages.length }}</b> 件在售商品</span>
      <span><b>{{ planCount }}</b> 个订阅计划</span>
      <span><b>{{ trafficCount }}</b> 个流量包</span>
      <span><b>{{ affordableCount }}</b> 件当前可购买</span>
    </div>
    <n-spin :show="loading">
    <div class="shop-grid">
      <div v-for="pkg in packages" :key="pkg.id" class="shop-card" :class="{ dim: !canAfford(pkg) }">
        <div class="sc-head">
          <div class="sc-name">{{ pkg.name }}</div>
          <span class="sc-badge" :class="typeMeta(pkg.type).cls">{{ typeMeta(pkg.type).label }}</span>
        </div>

        <div class="sc-desc" :class="{ empty: !pkg.description }">
          {{ pkg.description || '暂无套餐说明' }}
        </div>

        <ul v-if="pkg.highlights?.length" class="sc-highlights">
          <li v-for="(h, i) in pkg.highlights" :key="i">{{ h }}</li>
        </ul>

        <!-- 多时长套餐：选哪档，下面的规格和价格就跟着变 -->
        <div v-if="pkg.options?.length > 1" class="sc-durations">
          <button v-for="o in pkg.options" :key="o.days" type="button" class="sc-dur"
                  :class="{ on: chosenDays[pkg.id] === o.days }"
                  @click="chosenDays[pkg.id] = o.days">
            <span class="d">{{ o.days }} 天</span>
            <span class="p">{{ o.price_points }} 积分</span>
            <span v-if="saveHint(pkg, o)" class="off">{{ saveHint(pkg, o) }}</span>
          </button>
        </div>

        <div class="sc-specs">
          <div v-for="s in specsOf(pkg)" :key="s.label" class="sc-spec">
            <span class="k">{{ s.label }}</span>
            <span class="v">{{ s.value }}</span>
          </div>
        </div>

        <div class="sc-foot">
          <div v-if="willQueue(pkg)" class="sc-queue-note">✓ 同续期组正在使用 · 本次将排队，当前份结束后自动启用</div>
          <div class="sc-price">
            <span class="sc-points">{{ priceOf(pkg) }}</span>
            <span class="sc-unit">积分</span>
          </div>
          <div class="sc-yuan">{{ yuan(priceOf(pkg)) }}</div>
          <div v-if="pkg.stock >= 0" class="sc-stock" :class="{ hot: pkg.stock <= 5 }">
            {{ pkg.stock === 0 ? '已售罄' : `仅剩 ${pkg.stock} 件` }}
          </div>
          <n-button type="primary" block class="sc-buy"
            :loading="buying===pkg.id"
            :disabled="!canAfford(pkg) || pkg.stock === 0"
            @click="handleBuy(pkg)">
            {{ pkg.stock === 0 ? '已售罄' : canAfford(pkg) ? '购买' : '积分不足' }}
          </n-button>
        </div>
      </div>
    </div>
    <div v-if="!loading && packages.length===0" class="shop-empty">
      <n-empty description="暂无可购买的商品">
        <template #extra><n-button size="small" @click="loadPackages">重新加载</n-button></template>
      </n-empty>
      <div class="empty-guide">
        <span><b>套餐</b><small>同时包含流量与有效期</small></span>
        <span><b>流量包</b><small>为账户追加独立流量额度</small></span>
        <span><b>购买记录</b><small>商品上架后会在这里逐项对比</small></span>
      </div>
    </div>
    </n-spin>
  </div>
</template>
<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { NButton, NEmpty, NSpin, useMessage, useDialog } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import { apiList, apiPost } from '@/api'
import { fmtTotal, yuan } from '@/utils/format'
const auth = useAuthStore()
const message = useMessage()
const dialog = useDialog()
const packages = ref<any[]>([])
const loading = ref(false)
const buying = ref<number|null>(null)
const planCount = computed(() => packages.value.filter(p => p.type === 'plan').length)
const trafficCount = computed(() => packages.value.filter(p => p.type === 'traffic').length)
const affordableCount = computed(() => packages.value.filter(p => canAfford(p) && p.stock !== 0).length)
// Renewal-line keys the user already has an ACTIVE plan bucket for. Different
// product rows may share one queue_key, so package_id alone would wrongly promise
// immediate activation for a renamed/repriced edition of the same service.
const heldActiveQueues = ref<Set<string>>(new Set())
function packageQueueKey(pkg: any): string { return pkg.queue_key || `pkg:${pkg.id}` }
async function loadHeld() {
  try {
    const plans = await apiList<any>('/api/user/plans')
    heldActiveQueues.value = new Set(plans
      .filter((p: any) => p.kind === 'plan' && p.status === 'active' && p.package_id > 0)
      .map((p: any) => p.queue_key || `pkg:${p.package_id}`))
  } catch {}
}
function willQueue(pkg: any): boolean { return pkg.type === 'plan' && heldActiveQueues.value.has(packageQueueKey(pkg)) }

function typeMeta(type: string) {
  if (type === 'traffic') return { label: '流量包', cls: 't-traffic' }
  if (type === 'plan') return { label: '订阅计划', cls: 't-plan' }
  // 后端只认 traffic / plan（validPkgTypes）。留个中性兜底而不是硬套一个标签：
  // 真出现别的类型时，宁可原样显示，也不要谎报成某个具体品类。
  return { label: type || '套餐', cls: 't-other' }
}

// 一个套餐可以有多档时长（30/90/365 天…）。chosenDays 记住每张卡片当前选中的
// 那档，默认第一档；没有多档的套餐用套餐自身的字段，跟以前完全一样。
const chosenDays = ref<Record<number, number>>({})
function optOf(pkg: any) {
  const opts = pkg.options || []
  if (!opts.length) return { days: pkg.duration_days, price_points: pkg.price_points, traffic_bytes: pkg.traffic_bytes }
  return opts.find((o: any) => o.days === chosenDays.value[pkg.id]) || opts[0]
}
function priceOf(pkg: any): number { return optOf(pkg).price_points || 0 }

// saveHint 用第一档的单价当基准，标出长档便宜多少——不便宜就不标，免得凑数。
function saveHint(pkg: any, o: any): string {
  const base = pkg.options?.[0]
  if (!base?.days || !base.price_points || o.days === base.days) return ''
  const full = (base.price_points / base.days) * o.days
  const off = Math.round((1 - o.price_points / full) * 100)
  return off >= 1 ? `省${off}%` : ''
}

function canAfford(pkg: any): boolean {
  return (auth.user?.points || 0) >= priceOf(pkg)
}

// specsOf 为每张卡片生成对齐一致的规格行，方便用户逐项对比不同套餐。
function specsOf(pkg: any) {
  const opt = optOf(pkg)
  const s: { label: string; value: string }[] = []
  if (pkg.type === 'traffic' || pkg.type === 'plan') {
    s.push({ label: '流量', value: fmtTotal(opt.traffic_bytes) })
  }
  s.push({ label: '有效期', value: opt.days ? `${opt.days} 天` : '永久' })
  return s
}

function genKey(): string {
  try { if (crypto?.randomUUID) return crypto.randomUUID() } catch {}
  return 'k-' + Date.now() + '-' + Math.random().toString(36).slice(2)
}
async function purchaseWithRetry(packageId: number, days: number, key: string) {
  const body = { package_id: packageId, duration_days: days, idempotency_key: key }
  try {
    return await apiPost('/api/user/purchase', body)
  } catch (e: any) {
    // Retry ONCE on a network-level failure (an error with no HTTP status): the first
    // request may have committed server-side before its response was lost. Reusing the
    // same key makes the backend return the existing order instead of charging twice.
    if (e && e.status === undefined) {
      return await apiPost('/api/user/purchase', body)
    }
    throw e
  }
}
function handleBuy(pkg: any) {
  const queue = willQueue(pkg)
  const opt = optOf(pkg)
  const what = `「${pkg.name}」${pkg.options?.length > 1 ? `（${opt.days} 天）` : ''}`
  const content = queue
    ? `确定花费 ${opt.price_points} 积分购买${what}？\n同一续期组已有套餐正在使用，本次购买将排队，在当前份用完或到期后自动启用（有效期届时才开始计算）。`
    : `确定花费 ${opt.price_points} 积分购买${what}？\n购买成功后立即生效，有效期从购买成功时开始计算；只有同一续期组已有套餐时才会排队。`
  dialog.warning({ title: '确认购买', content, positiveText: '确定', negativeText: '取消',
    onPositiveClick: async () => {
      buying.value = pkg.id
      const key = genKey() // one key per confirmed purchase intent; stable across the retry
      try {
        // 单时长套餐照旧只报 package_id（duration_days=0 表示默认档）：万一后台刚
        // 改过天数，也不会因为页面上的旧值被判成「所选时长不可用」。
        await purchaseWithRetry(pkg.id, pkg.options?.length ? (opt.days || 0) : 0, key)
        message.success(queue ? '已购买并加入队列，将在当前套餐结束后自动启用' : '购买成功，已生效！')
        await auth.fetchMe(); await loadHeld()
      }
      catch (e: any) { message.error(e.message) } finally { buying.value = null }
    } })
}
async function loadPackages() {
  loading.value = true
  try {
    packages.value = await apiList('/api/user/packages')
    // 默认选中第一档，卡片一进来就有一档是高亮的
    for (const p of packages.value) if (p.options?.length) chosenDays.value[p.id] = p.options[0].days
  } catch (e: any) { message.error(e.message || '商品加载失败') }
  finally { loading.value = false }
}
onMounted(async () => {
  await loadPackages()
  loadHeld()
})
</script>
<style scoped>
.shop-head { display:flex; align-items:flex-start; justify-content:space-between; gap:20px; margin-bottom:16px; }
.shop-head .page-sub { margin-bottom:0; }
.balance-pill { display:grid; grid-template-columns:auto auto; align-items:baseline; gap:0 8px; min-width:156px; padding:10px 13px; border:1px solid var(--border); border-radius:12px; background:var(--card); box-shadow:var(--shadow-xs); text-align:right; }
.balance-pill small { grid-column:1 / -1; color:var(--text-3); font-size:10.5px; }
.balance-pill b { color:var(--text); font-size:20px; font-variant-numeric:tabular-nums; }
.balance-pill span { color:var(--text-3); font-size:11.5px; }
.shop-summary { display:flex; flex-wrap:wrap; gap:8px; margin-bottom:16px; }
.shop-summary span { padding:6px 10px; border:1px solid var(--border); border-radius:999px; background:var(--bg-soft); color:var(--text-3); font-size:11.5px; }
.shop-summary b { color:var(--text-2); }
.shop-empty { max-width:760px; margin:54px auto 0; padding:34px 30px 22px; border:1px solid var(--border); border-radius:16px; background:var(--card); box-shadow:var(--shadow-sm); }
.empty-guide { display:grid; grid-template-columns:repeat(3,1fr); gap:8px; margin-top:24px; padding-top:16px; border-top:1px solid var(--border); }
.empty-guide span { display:flex; flex-direction:column; padding:6px 9px; }
.empty-guide b { color:var(--text-2); font-size:12px; }
.empty-guide small { margin-top:2px; color:var(--text-3); font-size:10.5px; line-height:1.5; }

.shop-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }

.shop-card {
  display: flex;
  flex-direction: column;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--r);
  padding: 18px 18px 16px;
  transition: box-shadow .18s ease, transform .18s ease, border-color .18s ease;
}
.shop-card:hover { box-shadow: var(--shadow); border-color: #d8d8d8; transform: translateY(-2px); }
.shop-card.dim { opacity: .78; }

.sc-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; }
.sc-name { font-size: 16px; font-weight: 680; color: var(--text); line-height: 1.35; }

.sc-badge {
  flex: none;
  font-size: 11px;
  font-weight: 600;
  padding: 2px 9px;
  border-radius: 999px;
  white-space: nowrap;
  border: 1px solid transparent;
}
.t-traffic { color: var(--info); background: #eef2f6; border-color: #dde6ef; }
.t-plan { color: #4b7a5c; background: #edf4ef; border-color: #d9e8df; }
.t-other { color: var(--warn); background: #f7f1e2; border-color: #ece0c6; }

.sc-desc {
  margin-top: 10px;
  font-size: 13px;
  color: var(--text-2);
  line-height: 1.6;
  min-height: 41px; /* ~2 lines: keeps spec/price rows aligned across cards */
}
.sc-desc.empty { color: var(--text-3); }

.sc-highlights { list-style: none; margin: 12px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.sc-highlights li {
  position: relative;
  padding-left: 22px;
  font-size: 13px;
  color: var(--text);
  line-height: 1.5;
}
.sc-highlights li::before {
  content: "✓";
  position: absolute;
  left: 0;
  top: -1px;
  font-size: 12px;
  font-weight: 700;
  color: #4b7a5c;
}

/* 时长档位：整块可点，选中的一档描边加深，价格直接写在里面便于横向比较 */
.sc-durations {
  margin-top: 14px;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(84px, 1fr));
  gap: 8px;
}
.sc-dur {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  padding: 8px 6px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
  cursor: pointer;
  font: inherit;
  color: var(--text-2);
  transition: border-color .15s ease, background .15s ease, color .15s ease;
}
.sc-dur:hover { border-color: #c9c9c9; }
.sc-dur.on { border-color: var(--accent-strong); color: var(--text); background: #f6f9f7; }
.sc-dur .d { font-size: 13px; font-weight: 650; }
.sc-dur .p { font-size: 12px; color: var(--text-3); }
.sc-dur.on .p { color: var(--text-2); }
.sc-dur .off {
  position: absolute;
  top: -7px;
  right: -4px;
  font-size: 10px;
  line-height: 1;
  padding: 2px 5px;
  border-radius: 999px;
  color: #fff;
  background: var(--accent-strong);
}
@media (max-width: 560px) {
  .shop-head { align-items:stretch; flex-direction:column; }
  .balance-pill { text-align:left; align-self:flex-start; }
  .shop-empty { margin-top:26px; padding:28px 18px 18px; }
  .empty-guide { grid-template-columns:1fr; }
}

.sc-specs {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px dashed var(--border);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sc-spec { display: flex; justify-content: space-between; align-items: baseline; font-size: 13px; }
.sc-spec .k { color: var(--text-3); }
.sc-spec .v { color: var(--text); font-weight: 600; }

.sc-foot { margin-top: 16px; }
.sc-price { display: flex; align-items: baseline; gap: 6px; }
.sc-points { font-size: 24px; font-weight: 740; color: var(--accent-strong); letter-spacing: -.01em; }
.sc-unit { font-size: 13px; color: var(--text-2); }
.sc-yuan { font-size: 12px; color: var(--text-3); margin-top: 2px; }
.sc-stock { font-size: 11px; color: var(--text-3); margin-top: 6px; }
.sc-stock.hot { color: var(--warn); }
.sc-queue-note { font-size: 11px; color: #4b7a5c; background: #edf4ef; border: 1px solid #d9e8df; border-radius: 8px; padding: 5px 8px; margin-bottom: 10px; line-height: 1.4; }
.sc-buy { margin-top: 12px; }
</style>
