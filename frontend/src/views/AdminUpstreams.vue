<template>
  <div>
    <div class="page-head upstream-head">
      <div>
        <h2 class="page-title">上游管理</h2>
        <p class="page-sub">直接保存供应商账户凭据并查询官方用量；不经由节点、EdgeTunnel 或第三方中转。</p>
      </div>
      <n-button secondary :loading="loading" @click="load">刷新配置与余额</n-button>
    </div>

    <n-card size="small" class="homepage-toggle-card">
      <div class="homepage-toggle-row">
        <div>
          <div class="homepage-toggle-label">首页显示上游余额</div>
          <div class="homepage-toggle-hint">打开后仅管理员首页显示该卡片，不影响公开状态页。</div>
        </div>
        <n-switch
          :value="showOnHomepage"
          size="small"
          :disabled="homepageSettingSaving"
          @update:value="toggleHomepageVisible"
        />
      </div>
    </n-card>

    <n-alert type="info" :bordered="false" class="upstream-notice">
      OCI 直接查询 <b>Usage API</b> 当月已返回的出站用量，配置额度默认 10 TB/月，实际免费额度和计量范围需按账户合同核验。
      查询时间不代表官方数据已完整入账；参考余额不包含尚未返回的用量，不使用网卡流量或 EdgeTunnel 估算值替代。
    </n-alert>

    <n-spin :show="loading">
      <div class="upstream-grid">
        <div
          class="upstream-sort-item"
          :class="{ dragging: draggingProvider === 'oci', 'drag-over': dragOverProvider === 'oci' }"
          :style="{ order: upstreamOrder.indexOf('oci') }"
          draggable="true"
          @dragstart="handleProviderDragStart('oci', $event)"
          @dragover.prevent="handleProviderDragOver('oci', $event)"
          @drop.prevent="handleProviderDrop('oci')"
          @dragend="handleProviderDragEnd"
        >
        <n-card size="small" class="upstream-card" title="Oracle Cloud Infrastructure">
          <template #header-extra><n-tag :type="tagType(ociView.configured)" size="small" :bordered="false">{{ ociView.configured ? '已配置' : '未配置' }}</n-tag></template>
          <div class="balance-panel" :class="usageClass(ociUsage)">
            <template v-if="ociUsage?.success">
              <div class="balance-kicker">{{ ociUsage.period }}账户余额</div>
              <div class="balance-value">{{ fmtBytes(ociUsage.remaining) }}</div>
              <div class="balance-meta">账号总额 {{ fmtBytes(ociUsage.limit) }} − 官方已用 {{ fmtBytes(ociUsage.used) }}<template v-if="ociUsage.overage_detected"> · 已识别超额层级</template></div>
              <n-progress type="line" :percentage="usagePercent(ociUsage)" :show-indicator="false" :height="6" status="success" />
              <div class="balance-source">{{ ociUsage.source }} · 查询区间结束 {{ fmtUpdated(ociUsage.query_end) }} · 查询于 {{ fmtUpdated(ociUsage.updated_at) }}</div>
              <n-alert v-if="ociUsage.warning" type="warning" :bordered="false">{{ ociUsage.warning }}</n-alert>
            </template>
            <template v-else>
              <div class="balance-kicker">OCI 官方账户用量</div>
              <div class="balance-empty">{{ ociUsage?.error || '保存配置后即可查询' }}</div>
              <div class="balance-source">数据源：OCI Usage API</div>
            </template>
          </div>

          <n-form label-placement="top" class="provider-form">
            <n-form-item label="Tenancy OCID"><n-input v-model:value="ociForm.tenancy_ocid" placeholder="ocid1.tenancy..." /></n-form-item>
            <n-form-item label="User OCID"><n-input v-model:value="ociForm.user_ocid" placeholder="ocid1.user..." /></n-form-item>
            <div class="form-row">
              <n-form-item label="API Key 指纹"><n-input v-model:value="ociForm.fingerprint" placeholder="aa:bb:cc..." /></n-form-item>
              <n-form-item label="区域"><n-input v-model:value="ociForm.region" placeholder="ap-tokyo-1" /></n-form-item>
            </div>
            <n-form-item label="API 私钥">
              <n-input v-model:value="ociForm.private_key" type="textarea" :autosize="{ minRows: 3, maxRows: 7 }" placeholder="粘贴 RSA PKCS#1 或 PKCS#8 PEM 私钥；留空保留当前私钥" />
              <div class="secret-state">{{ ociView.private_key_set ? '当前私钥已加密保存。' : '尚未保存私钥。' }}</div>
            </n-form-item>
            <n-form-item label="月度上限（字节）">
              <n-input-number v-model:value="ociForm.monthly_limit_bytes" :min="1" :max="1000000000000000" :show-button="false" style="width:100%;" />
              <div class="field-note">当前：{{ fmtBytes(ociForm.monthly_limit_bytes) }}。配置默认 10 TB/月（十进制）；请按账户实际额度确认。</div>
            </n-form-item>
          </n-form>
          <div class="provider-actions">
            <n-button type="primary" :loading="saving.oci" @click="saveOCI">保存 OCI 配置</n-button>
            <n-button :disabled="!ociView.configured" :loading="refreshing.oci" @click="refreshUsage('oci')">查询余额</n-button>
            <n-button v-if="ociView.configured" tertiary type="error" @click="removeProvider('oci')">清除</n-button>
          </div>
        </n-card>
        </div>

        <div
          class="upstream-sort-item"
          :class="{ dragging: draggingProvider === 'cloudflare', 'drag-over': dragOverProvider === 'cloudflare' }"
          :style="{ order: upstreamOrder.indexOf('cloudflare') }"
          draggable="true"
          @dragstart="handleProviderDragStart('cloudflare', $event)"
          @dragover.prevent="handleProviderDragOver('cloudflare', $event)"
          @drop.prevent="handleProviderDrop('cloudflare')"
          @dragend="handleProviderDragEnd"
        >
        <n-card size="small" class="upstream-card" title="Cloudflare">
          <template #header-extra><n-tag :type="tagType(cfView.configured)" size="small" :bordered="false">{{ cfView.configured ? '已配置' : '未配置' }}</n-tag></template>
          <div class="balance-panel" :class="usageClass(cfUsage)">
            <template v-if="cfUsage?.success">
              <div class="balance-kicker">{{ cfUsage.period }}请求余额</div>
              <div class="balance-value">{{ fmtRequests(cfUsage.remaining) }}</div>
              <div class="balance-meta">已用 {{ fmtRequests(cfUsage.used) }} / 上限 {{ fmtRequests(cfUsage.limit) }}</div>
              <n-progress type="line" :percentage="usagePercent(cfUsage)" :show-indicator="false" :height="6" status="success" />
              <div class="balance-source">{{ cfUsage.source }} · {{ fmtUpdated(cfUsage.updated_at) }}</div>
            </template>
            <template v-else>
              <div class="balance-kicker">Cloudflare 官方账户用量</div>
              <div class="balance-empty">{{ cfUsage?.error || '保存配置后即可查询' }}</div>
              <div class="balance-source">数据源：Cloudflare Analytics GraphQL</div>
            </template>
          </div>

          <n-form label-placement="top" class="provider-form">
            <n-form-item label="Account ID"><n-input v-model:value="cfForm.account_id" placeholder="Cloudflare 账户 ID" /></n-form-item>
            <n-form-item label="Analytics Token">
              <n-input v-model:value="cfForm.analytics_token" type="password" show-password-on="click" placeholder="留空保留当前 Token" />
              <div class="secret-state">{{ cfView.analytics_token_set ? '当前 Token 已加密保存。' : '尚未保存 Token。' }}</div>
            </n-form-item>
            <n-form-item label="每日请求上限">
              <n-input-number v-model:value="cfForm.daily_request_limit" :min="1" :max="10000000000" :show-button="false" style="width:100%;" />
              <div class="field-note">当前：{{ fmtRequests(cfForm.daily_request_limit) }}。默认 100,000 次/日；只用于计算余额。</div>
            </n-form-item>
          </n-form>
          <div class="provider-actions">
            <n-button type="primary" :loading="saving.cloudflare" @click="saveCloudflare">保存 Cloudflare 配置</n-button>
            <n-button :disabled="!cfView.configured" :loading="refreshing.cloudflare" @click="refreshUsage('cloudflare')">查询余额</n-button>
            <n-button v-if="cfView.configured" tertiary type="error" @click="removeProvider('cloudflare')">清除</n-button>
          </div>
        </n-card>
        </div>
      </div>

      <n-card size="small" class="permission-card" title="供应商权限与口径">
        <div class="permission-grid">
          <div><b>OCI</b><span>在 OCI Console 的用户 API Key 中上传对应公钥。此页仅请求该 tenancy 的 Usage API，按当前 UTC 月筛选数据传输 / 出站 / egress 条目。</span></div>
          <div><b>Cloudflare</b><span>新建专用 API Token 并授予 Account Analytics Read。不要填写 ACME/DNS Token；请求数为账户级 Pages Functions + Workers 总和。</span></div>
        </div>
      </n-card>
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NInputNumber, NProgress, NSpin, NSwitch, NTag, useDialog, useMessage } from 'naive-ui'
import { apiDelete, apiGet, apiPost, apiPut } from '@/api'
import { fmtBytes } from '@/utils/format'

type Provider = 'oci' | 'cloudflare'
type ProviderView = {
  provider: Provider
  configured: boolean
  tenancy_ocid?: string
  user_ocid?: string
  fingerprint?: string
  region?: string
  account_id?: string
  limit: number
  private_key_set?: boolean
  analytics_token_set?: boolean
}
type Usage = {
  configured: boolean
  success: boolean
  used: number
  limit: number
  remaining: number
  unit: string
  period: string
  source: string
  query_end?: string
  warning?: string
  overage_detected?: boolean
  updated_at?: string
  error?: string
}

const message = useMessage()
const dialog = useDialog()
const loading = ref(false)
const saving = reactive<Record<Provider, boolean>>({ oci: false, cloudflare: false })
const refreshing = reactive<Record<Provider, boolean>>({ oci: false, cloudflare: false })
const ociView = reactive<ProviderView>({ provider: 'oci', configured: false, limit: 10_000_000_000_000 })
const cfView = reactive<ProviderView>({ provider: 'cloudflare', configured: false, limit: 100_000 })
const usages = reactive<Partial<Record<Provider, Usage>>>({})
const ociForm = reactive({ tenancy_ocid: '', user_ocid: '', fingerprint: '', region: '', private_key: '', monthly_limit_bytes: 10_000_000_000_000 })
const cfForm = reactive({ account_id: '', analytics_token: '', daily_request_limit: 100_000 })

const ociUsage = computed(() => usages.oci)
const cfUsage = computed(() => usages.cloudflare)
const upstreamOrder = ref<Provider[]>(['oci', 'cloudflare'])
const draggingProvider = ref<Provider | null>(null)
const dragOverProvider = ref<Provider | null>(null)
const showOnHomepage = ref(false)
const homepageSettingSaving = ref(false)

function assignView(target: ProviderView, source?: ProviderView) {
  Object.assign(target, { provider: target.provider, configured: false, limit: target.provider === 'oci' ? 10_000_000_000_000 : 100_000 }, source || {})
}
function setForms() {
  Object.assign(ociForm, {
    tenancy_ocid: ociView.tenancy_ocid || '', user_ocid: ociView.user_ocid || '', fingerprint: ociView.fingerprint || '', region: ociView.region || '',
    private_key: '', monthly_limit_bytes: ociView.limit || 10_000_000_000_000,
  })
  Object.assign(cfForm, { account_id: cfView.account_id || '', analytics_token: '', daily_request_limit: cfView.limit || 100_000 })
}
async function load() {
  loading.value = true
  try {
    const [views, settings] = await Promise.all([
      apiGet<ProviderView[]>('/api/admin/upstreams'),
      apiGet<Record<string, string>>('/api/admin/settings'),
    ])
    assignView(ociView, views.find(v => v.provider === 'oci'))
    assignView(cfView, views.find(v => v.provider === 'cloudflare'))
    setForms()
    upstreamOrder.value = normalizeProviderOrder(settings?.admin_upstream_balance_order)
    showOnHomepage.value = settings?.admin_upstream_balance_visible === 'true'
    await Promise.all([ociView.configured ? refreshUsage('oci', true) : Promise.resolve(), cfView.configured ? refreshUsage('cloudflare', true) : Promise.resolve()])
  } catch (error: any) {
    message.error(error.message || '读取上游配置失败')
  } finally { loading.value = false }
}

function normalizeProviderOrder(raw: unknown): Provider[] {
  const values = Array.isArray(raw) ? raw : String(raw || '').split(',')
  const valid = values.filter((value): value is Provider => value === 'oci' || value === 'cloudflare')
  return [...new Set<Provider>([...valid, 'oci', 'cloudflare'])]
}
function handleProviderDragStart(provider: Provider, event: DragEvent) {
  draggingProvider.value = provider
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', provider)
  }
}
function handleProviderDragOver(provider: Provider, event: DragEvent) {
  if (!draggingProvider.value || draggingProvider.value === provider) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  dragOverProvider.value = provider
}
async function handleProviderDrop(provider: Provider) {
  const source = draggingProvider.value
  dragOverProvider.value = null
  if (!source || source === provider) return
  const previous = [...upstreamOrder.value]
  const next = [...previous]
  const from = next.indexOf(source)
  if (from < 0 || !next.includes(provider)) return
  const target = next.indexOf(provider)
  ;[next[from], next[target]] = [next[target], next[from]]
  upstreamOrder.value = next
  try {
    await apiPut('/api/admin/settings', { admin_upstream_balance_order: next.join(',') })
  } catch (error: any) {
    upstreamOrder.value = previous
    message.error(error.message || '保存余额卡片顺序失败')
  }
}
function handleProviderDragEnd() {
  draggingProvider.value = null
  dragOverProvider.value = null
}
async function toggleHomepageVisible(value: boolean) {
  const previous = showOnHomepage.value
  showOnHomepage.value = value
  homepageSettingSaving.value = true
  try {
    await apiPut('/api/admin/settings', { admin_upstream_balance_visible: String(value) })
    message.success(value ? '已在首页显示上游余额' : '已从首页隐藏上游余额')
  } catch (error: any) {
    showOnHomepage.value = previous
    message.error(error.message || '保存首页显示设置失败')
  } finally {
    homepageSettingSaving.value = false
  }
}
async function refreshUsage(provider: Provider, quiet = false) {
  refreshing[provider] = true
  try {
    usages[provider] = await apiPost<Usage>(`/api/admin/upstreams/${provider}/refresh`, undefined, { timeoutMs: 30_000 })
    if (!usages[provider]?.success && !quiet) message.warning(usages[provider]?.error || '官方接口未返回可用数据')
  } catch (error: any) {
    usages[provider] = { configured: true, success: false, used: 0, limit: provider === 'oci' ? ociForm.monthly_limit_bytes : cfForm.daily_request_limit, remaining: 0, unit: provider === 'oci' ? 'bytes' : 'requests', period: '', source: '', error: error.message || '查询失败' }
    if (!quiet) message.error(error.message || '查询失败')
  } finally { refreshing[provider] = false }
}
let usageRefreshTimer: number | undefined
function startUsageRefresh() {
  if (usageRefreshTimer !== undefined) window.clearInterval(usageRefreshTimer)
  usageRefreshTimer = window.setInterval(() => {
    if (document.visibilityState === 'hidden') return
    if (ociView.configured) refreshUsage('oci', true)
    if (cfView.configured) refreshUsage('cloudflare', true)
  }, 15 * 60 * 1000)
  document.addEventListener('visibilitychange', handleVisibilityRefresh)
}
function handleVisibilityRefresh() {
  if (document.visibilityState !== 'visible') return
  if (ociView.configured) refreshUsage('oci', true)
  if (cfView.configured) refreshUsage('cloudflare', true)
}
async function saveOCI() {
  saving.oci = true
  try {
    const view = await apiPut<ProviderView>('/api/admin/upstreams/oci', ociForm)
    assignView(ociView, view)
    ociForm.private_key = ''
    message.success('OCI 上游配置已保存')
    await refreshUsage('oci', true)
  } catch (error: any) { message.error(error.message || '保存 OCI 配置失败') }
  finally { saving.oci = false }
}
async function saveCloudflare() {
  saving.cloudflare = true
  try {
    const view = await apiPut<ProviderView>('/api/admin/upstreams/cloudflare', cfForm)
    assignView(cfView, view)
    cfForm.analytics_token = ''
    message.success('Cloudflare 上游配置已保存')
    await refreshUsage('cloudflare', true)
  } catch (error: any) { message.error(error.message || '保存 Cloudflare 配置失败') }
  finally { saving.cloudflare = false }
}
function removeProvider(provider: Provider) {
  const label = provider === 'oci' ? 'OCI' : 'Cloudflare'
  dialog.warning({
    title: `清除 ${label} 上游配置`, content: `会删除已加密保存的 ${label} 凭据与上限，无法恢复。确定继续？`, positiveText: '清除', negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await apiDelete(`/api/admin/upstreams/${provider}`)
        delete usages[provider]
        if (provider === 'oci') {
          assignView(ociView)
          Object.assign(ociForm, { tenancy_ocid: '', user_ocid: '', fingerprint: '', region: '', private_key: '', monthly_limit_bytes: 10_000_000_000_000 })
        } else {
          assignView(cfView)
          Object.assign(cfForm, { account_id: '', analytics_token: '', daily_request_limit: 100_000 })
        }
        message.success(`${label} 上游配置已清除`)
      } catch (error: any) { message.error(error.message || '清除失败') }
    },
  })
}
function tagType(configured: boolean): 'success' | 'default' { return configured ? 'success' : 'default' }
function usageClass(usage?: Usage) { return usage?.success ? 'ready' : usage?.error ? 'failed' : '' }
function usagePercent(usage?: Usage) { return usage?.limit ? Math.min(100, Math.max(0, Math.round(usage.used / usage.limit * 1000) / 10)) : 0 }
function fmtRequests(value?: number) { return new Intl.NumberFormat('zh-CN').format(value || 0) + ' 次' }
function fmtUpdated(value?: string) { return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '尚未更新' }

onMounted(async () => {
  await load()
  startUsageRefresh()
})
onUnmounted(() => {
  if (usageRefreshTimer !== undefined) window.clearInterval(usageRefreshTimer)
  document.removeEventListener('visibilitychange', handleVisibilityRefresh)
})
</script>

<style scoped>
.upstream-head { margin-bottom: 14px; }
.homepage-toggle-card { margin-bottom: 14px; }
.homepage-toggle-card :deep(.n-card__content) { padding: 12px 14px; }
.homepage-toggle-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.homepage-toggle-label { font-size: 13px; font-weight: 650; color: var(--text); }
.homepage-toggle-hint { margin-top: 3px; font-size: 12px; color: var(--text-3); line-height: 1.55; }
.upstream-notice { margin-bottom: 16px; line-height: 1.75; }
.upstream-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; align-items: start; }
.upstream-sort-item { min-width: 0; order: 0; cursor: grab; transition: opacity .2s ease, transform .2s ease; }
.upstream-sort-item:active { cursor: grabbing; }
.upstream-sort-item.dragging { opacity: .45; transform: scale(.99); }
.upstream-sort-item.drag-over { border-radius: var(--r-sm); box-shadow: 0 0 0 2px var(--accent-soft); }
.upstream-card { min-width: 0; }
.balance-panel { padding: 14px; margin-bottom: 14px; border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--bg-soft); min-height: 122px; }
.balance-panel.ready { background: linear-gradient(135deg, rgba(233, 242, 236, .78), var(--card)); border-color: rgba(76, 113, 85, .2); }
.balance-panel.failed { background: linear-gradient(135deg, rgba(250, 236, 234, .75), var(--card)); border-color: rgba(168, 86, 75, .18); }
.balance-kicker, .balance-source, .secret-state, .field-note { font-size: 12px; color: var(--text-3); line-height: 1.65; }
.balance-value { margin: 4px 0 1px; font-size: 28px; line-height: 1.18; font-weight: 720; letter-spacing: -.025em; color: var(--text); font-variant-numeric: tabular-nums; }
.balance-meta { margin-bottom: 8px; font-size: 12.5px; color: var(--text-2); font-variant-numeric: tabular-nums; }
.balance-source { margin-top: 8px; }
.balance-empty { margin: 9px 0; min-height: 26px; font-size: 14px; color: var(--text-2); line-height: 1.55; }
.provider-form :deep(.n-form-item) { margin-bottom: 12px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.provider-actions { display: flex; flex-wrap: wrap; gap: 8px; padding-top: 2px; }
.permission-card { margin-top: 14px; }
.permission-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.permission-grid div { padding: 2px 2px; }
.permission-grid b { display: block; margin-bottom: 5px; font-size: 13px; }
.permission-grid span { display: block; color: var(--text-2); font-size: 12px; line-height: 1.75; }
@media (max-width: 900px) { .upstream-grid, .permission-grid { grid-template-columns: 1fr; } }
@media (max-width: 520px) { .form-row { grid-template-columns: 1fr; gap: 0; } .provider-actions :deep(.n-button) { flex: 1; } }
</style>
