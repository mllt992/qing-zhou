<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">API / MCP Token</h2>
        <p class="page-sub">给 AI 客户端（Remote MCP /mcp）、脚本或外部运维用的机器身份：可收窄 scope、可设有效期、可随时吊销</p>
      </div>
    </div>

    <div class="resource-overview">
      <div class="resource-metric"><b>{{ tokens.length }}</b><span>全部已签发令牌</span></div>
      <div class="resource-metric success"><b>{{ activeCount }}</b><span>当前有效令牌</span></div>
      <div class="resource-metric"><b>{{ mcpTokenCount }}</b><span>具备 MCP 权限</span></div>
      <div class="resource-metric"><b>{{ auditLogs.length }}</b><span>最近 MCP 审计记录</span></div>
    </div>

    <!-- 创建令牌面板 -->
    <n-card size="small" class="token-create-card" style="margin-bottom:16px;">
      <template #header>
        <div class="tcc-head">
          <span>签发新令牌</span>
          <span class="tcc-warn">⚠️ 请勿将 <code>backup:write</code> 或 <code>mcp:settings</code> 授予不可信环境；明文仅在创建时显示一次</span>
        </div>
      </template>
      <template #header-extra>
        <div class="preset-group">
          <span class="preset-label">快捷预设：</span>
          <button type="button" class="preset-btn" :class="{ active: currentPreset === 'mcp-readonly' }" @click="applyPreset('mcp-readonly')">AI 只读监控</button>
          <button type="button" class="preset-btn" :class="{ active: currentPreset === 'mcp-ops' }" @click="applyPreset('mcp-ops')">AI 日常运维</button>
          <button type="button" class="preset-btn" :class="{ active: currentPreset === 'mcp-admin' }" @click="applyPreset('mcp-admin')">AI 全量运维 (推荐)</button>
        </div>
      </template>

      <div class="tcc-form-row">
        <div class="tcc-field name-field">
          <label>令牌名称</label>
          <n-input v-model:value="form.name" placeholder="如 NAS / claude-code / telegram-bot" @keyup.enter="createToken" />
        </div>
        <div class="tcc-field ttl-field">
          <label>有效期</label>
          <n-select v-model:value="form.ttl" :options="ttlOpts" />
        </div>
        <div class="tcc-action">
          <n-button type="primary" :loading="creating" @click="createToken">签发 Token</n-button>
        </div>
      </div>

      <!-- Scope 权限矩阵：替代原本撑爆高度的多选下拉框 -->
      <div class="scope-section">
        <div class="scope-sec-head">
          <span>权限范围（Scopes · 已选 <b>{{ form.scopes.length }}</b> 项）</span>
          <div class="scope-quick-links">
            <button type="button" @click="selectAllMcp">全选 MCP</button>
            <span>·</span>
            <button type="button" @click="form.scopes = ['mcp:read']">仅只读</button>
          </div>
        </div>
        <div class="scope-grid">
          <button
            v-for="s in scopeCatalog"
            :key="s.value"
            type="button"
            class="scope-item"
            :class="{ checked: form.scopes.includes(s.value), sensitive: s.sensitive }"
            @click="toggleScope(s.value)"
          >
            <div class="si-top">
              <span class="si-check">{{ form.scopes.includes(s.value) ? '✓' : '' }}</span>
              <code class="si-code">{{ s.value }}</code>
              <span v-if="s.sensitive" class="si-sens">高权限</span>
            </div>
            <div class="si-desc">{{ s.desc }}</div>
          </button>
        </div>
      </div>

      <!-- 新签发明文与 MCP JSON 面板 -->
      <div v-if="createdPlain" class="created-box">
        <div class="cb-block">
          <div class="cb-head">
            <span class="cb-title warn">🔑 请立即复制保存，离开页面后无法再次查看明文密钥</span>
            <n-button size="tiny" type="primary" @click="copyPlain">复制 Token</n-button>
          </div>
          <code class="cb-token">{{ createdPlain }}</code>
        </div>
        <div v-if="createdMcpConfigJson" class="cb-block cb-mcp">
          <div class="cb-head">
            <div>
              <span class="cb-title">Remote MCP 客户端配置（可直接粘贴到 Claude Code / Antigravity / Cursor）</span>
              <div class="cb-endpoint">端点地址：<code>{{ createdMcpUrl }}</code></div>
            </div>
            <n-button size="tiny" type="primary" secondary @click="copyMcpConfig">复制 MCP JSON</n-button>
          </div>
          <pre class="cb-pre">{{ createdMcpConfigJson }}</pre>
        </div>
      </div>
    </n-card>

    <!-- 已签发 Token 列表 -->
    <n-spin :show="loading">
      <div v-if="tokens.length" class="token-grid">
        <div v-for="t in tokens" :key="t.id" class="token-card" :class="{ revoked: !!t.revoked_at }">
          <div class="tc-head">
            <div class="tc-title-row">
              <span class="tc-icon">🔑</span>
              <span class="tc-name">{{ t.name }}</span>
              <code class="tc-prefix">{{ t.prefix }}…</code>
            </div>
            <div class="tc-head-right">
              <n-tag :type="statusType(t)" size="small" :bordered="false">{{ statusLabel(t) }}</n-tag>
              <n-button v-if="!t.revoked_at" size="tiny" type="error" secondary @click="revoke(t)">吊销</n-button>
            </div>
          </div>

          <div class="tc-scopes">
            <code v-for="sc in (t.scopes || [])" :key="sc" class="tc-scope-chip" :class="{ mcp: String(sc).startsWith('mcp:') }">{{ sc }}</code>
          </div>

          <div class="tc-meta-grid">
            <div class="tc-meta-cell">
              <span class="k">签发时间</span>
              <span class="v">{{ fmtDateTime(t.created_at) }}</span>
            </div>
            <div class="tc-meta-cell">
              <span class="k">过期时间</span>
              <span class="v">{{ t.expires_at ? fmtDateTime(t.expires_at) : '永不过期' }}</span>
            </div>
            <div class="tc-meta-cell">
              <span class="k">最近调用</span>
              <span class="v">{{ t.last_used_at ? fmtDateTime(t.last_used_at) : '从未调用' }}</span>
            </div>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无已签发的 API Token" style="padding:36px 0;" />
    </n-spin>

    <!-- 最近 MCP 调用审计日志 -->
    <n-card size="small" style="margin-top:18px;" title="最近 MCP 调用审计日志">
      <template #header-extra>
        <n-button size="tiny" secondary :loading="auditLoading" @click="loadAuditLogs">刷新日志</n-button>
      </template>
      <n-spin :show="auditLoading">
        <div v-if="auditLogs.length" class="audit-list">
          <div v-for="log in pagedAuditLogs" :key="log.id" class="audit-row">
            <div class="ar-top">
              <n-tag :type="log.status === 'ok' ? 'success' : (log.status === 'denied' ? 'warning' : 'error')" size="tiny" :bordered="false">
                {{ log.status }}
              </n-tag>
              <code class="ar-tool">{{ log.tool_name }}</code>
              <span class="ar-meta">令牌：<b>{{ log.token_name || ('#' + log.token_id) }}</b></span>
              <span class="ar-meta">{{ log.duration_ms }} ms</span>
              <span class="ar-time">{{ fmtDateTime(log.created_at) }}</span>
            </div>
            <div v-if="log.args_summary" class="ar-args">{{ log.args_summary }}</div>
            <div v-if="log.error_msg" class="ar-err">{{ log.error_msg }}</div>
          </div>
          <div v-if="auditLogs.length > auditPageSize" class="qz-pager" style="margin-top:10px;">
            <span class="qz-pager-info">共 <b>{{ auditLogs.length }}</b> 条审计记录</span>
            <n-pagination v-model:page="auditPage" :page-size="auditPageSize" :item-count="auditLogs.length" size="small" />
          </div>
        </div>
        <n-empty v-else description="暂无 MCP 调用记录" style="padding:28px 0;" />
      </n-spin>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { NCard, NInput, NButton, NTag, NSpin, NSelect, NEmpty, NPagination, useMessage, useDialog } from 'naive-ui'
import { apiGet, apiList, apiPost, apiDelete } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

const message = useMessage()
const dialog = useDialog()
const tokens = ref<any[]>([])
const loading = ref(false)
const auditLogs = ref<any[]>([])
const auditLoading = ref(false)
const auditPage = ref(1)
const auditPageSize = 10
const creating = ref(false)
const createdPlain = ref('')
const createdMcpUrl = ref('')
const createdMcpConfigJson = ref('')

const scopeCatalog = [
  { value: 'mcp:read', desc: 'MCP 只读：监控、流量与诊断', sensitive: false },
  { value: 'mcp:nodes', desc: 'MCP 节点：TLS、入站、节点与分组', sensitive: false },
  { value: 'mcp:servers', desc: 'MCP 服务器：SSH 探测、重装与同步', sensitive: false },
  { value: 'mcp:users', desc: 'MCP 用户：账号与套餐分配运维', sensitive: false },
  { value: 'mcp:settings', desc: 'MCP 设置：系统参数与 Telegram', sensitive: true },
  { value: 'stats:read', desc: 'REST 统计只读接口', sensitive: false },
  { value: 'users:read', desc: 'REST 用户列表只读', sensitive: false },
  { value: 'nodes:read', desc: 'REST 节点列表只读', sensitive: false },
  { value: 'servers:read', desc: 'REST 服务器列表只读', sensitive: false },
  { value: 'backup:write', desc: 'REST 数据库热备份导出', sensitive: true },
]

const ttlOpts = [
  { label: '永不过期', value: 0 },
  { label: '7 天', value: 7 * 86400 },
  { label: '30 天', value: 30 * 86400 },
  { label: '90 天', value: 90 * 86400 },
  { label: '365 天', value: 365 * 86400 },
]
const form = reactive({ name: '', scopes: ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users', 'mcp:settings'] as string[], ttl: 0 })

const activeCount = computed(() => tokens.value.filter(t => !t.revoked_at && (!t.expires_at || t.expires_at * 1000 >= Date.now())).length)
const mcpTokenCount = computed(() => tokens.value.filter(t => (t.scopes || []).some((s: string) => s.startsWith('mcp:'))).length)
const pagedAuditLogs = computed(() => {
  const start = (auditPage.value - 1) * auditPageSize
  return auditLogs.value.slice(start, start + auditPageSize)
})

const currentPreset = computed(() => {
  const s = new Set(form.scopes)
  if (s.size === 1 && s.has('mcp:read')) return 'mcp-readonly'
  if (s.size === 4 && ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users'].every(k => s.has(k))) return 'mcp-ops'
  if (s.size === 5 && ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users', 'mcp:settings'].every(k => s.has(k))) return 'mcp-admin'
  return ''
})

function toggleScope(val: string) {
  const idx = form.scopes.indexOf(val)
  if (idx >= 0) form.scopes.splice(idx, 1)
  else form.scopes.push(val)
}
function selectAllMcp() {
  form.scopes = ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users', 'mcp:settings']
}

function applyPreset(preset: 'mcp-readonly' | 'mcp-ops' | 'mcp-admin') {
  if (preset === 'mcp-readonly') {
    form.scopes = ['mcp:read']
    if (!form.name.trim() || ['ai-ops', 'ai-admin'].includes(form.name)) form.name = 'ai-readonly'
  } else if (preset === 'mcp-ops') {
    form.scopes = ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users']
    if (!form.name.trim() || ['ai-readonly', 'ai-admin'].includes(form.name)) form.name = 'ai-ops'
  } else {
    form.scopes = ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users', 'mcp:settings']
    if (!form.name.trim() || ['ai-readonly', 'ai-ops'].includes(form.name)) form.name = 'ai-admin'
  }
}

function statusLabel(t: any) {
  if (t.revoked_at) return '已吊销'
  if (t.expires_at && t.expires_at * 1000 < Date.now()) return '已过期'
  return '有效'
}
function statusType(t: any): 'success' | 'warning' | 'error' | 'default' {
  if (t.revoked_at) return 'error'
  if (t.expires_at && t.expires_at * 1000 < Date.now()) return 'warning'
  return 'success'
}

async function load() {
  loading.value = true
  try { tokens.value = await apiList('/api/admin/tokens') || [] }
  catch (e: any) { message.error(e.message) }
  finally { loading.value = false }
}

async function createToken() {
  if (!form.name.trim()) { message.warning('请填写令牌名称'); return }
  if (!form.scopes.length) { message.warning('请至少勾选一项权限 Scope'); return }
  creating.value = true
  createdPlain.value = ''
  createdMcpUrl.value = ''
  createdMcpConfigJson.value = ''
  try {
    const data = await apiPost<any>('/api/admin/tokens', {
      name: form.name.trim(),
      scopes: form.scopes,
      expires_in: form.ttl || 0,
    })
    createdPlain.value = data.token
    if (data.mcp_config) {
      createdMcpUrl.value = data.mcp_url || `${window.location.origin}/mcp`
      createdMcpConfigJson.value = JSON.stringify(data.mcp_config, null, 2)
    }
    form.name = ''
    message.success('已创建，请立即保存明文 Token / MCP 配置')
    await load()
  } catch (e: any) { message.error(e.message) }
  finally { creating.value = false }
}

function copyPlain() {
  copyText(createdPlain.value)
  message.success('已复制 Token')
}

function copyMcpConfig() {
  copyText(createdMcpConfigJson.value)
  message.success('已复制 MCP 配置 JSON')
}

function revoke(t: any) {
  dialog.warning({
    title: '吊销 Token',
    content: `确定吊销「${t.name}」？使用该 Token 的集成会立即失效。`,
    positiveText: '吊销', negativeText: '取消',
    onPositiveClick: async () => {
      try {
        await apiDelete('/api/admin/tokens/' + t.id)
        message.success('已吊销')
        await load()
      } catch (e: any) { message.error(e.message) }
    },
  })
}

async function loadAuditLogs() {
  auditLoading.value = true
  try {
    const res = await apiGet<any>('/api/admin/mcp/audit-logs?limit=50')
    auditLogs.value = res?.rows || []
  } catch {} finally { auditLoading.value = false }
}

onMounted(() => { load(); loadAuditLogs() })
</script>

<style scoped>
.tcc-head { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.tcc-warn {
  font-size: 11.5px; font-weight: 500; color: #b45309;
  background: var(--warn-soft); border: 1px solid rgba(217, 119, 6, .18);
  padding: 2px 10px; border-radius: 999px;
}
.tcc-warn code { font-family: var(--mono); font-weight: 650; }

.preset-group { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.preset-label { font-size: 12px; color: var(--text-3); }
.preset-btn {
  padding: 4px 10px; border-radius: 7px;
  border: 1px solid var(--border); background: var(--bg-soft);
  color: var(--text-2); font: inherit; font-size: 12px; font-weight: 560;
  cursor: pointer; transition: all .18s ease;
}
.preset-btn:hover { border-color: var(--border-strong); color: var(--text); }
.preset-btn.active {
  background: var(--accent-soft); color: var(--accent-strong);
  border-color: rgba(15, 108, 189, .26); font-weight: 650;
}

.tcc-form-row {
  display: grid;
  grid-template-columns: minmax(220px, 1.5fr) 180px auto;
  gap: 12px;
  align-items: end;
  margin-bottom: 16px;
}
@media (max-width: 720px) {
  .tcc-form-row { grid-template-columns: 1fr; }
}
.tcc-field { display: flex; flex-direction: column; gap: 5px; }
.tcc-field label { font-size: 12px; font-weight: 600; color: var(--text-2); }

.scope-section {
  padding-top: 12px;
  border-top: 1px solid var(--border);
}
.scope-sec-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 10px; font-size: 12px; color: var(--text-2);
}
.scope-sec-head b { color: var(--accent-strong); }
.scope-quick-links { display: flex; align-items: center; gap: 6px; color: var(--text-3); }
.scope-quick-links button {
  border: 0; background: none; padding: 0; font: inherit; font-size: 12px;
  color: var(--accent); cursor: pointer;
}
.scope-quick-links button:hover { text-decoration: underline; }

.scope-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(215px, 1fr));
  gap: 8px;
}
.scope-item {
  display: flex; flex-direction: column; gap: 4px;
  padding: 9px 11px;
  text-align: left; font: inherit; cursor: pointer;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 9px;
  transition: all .16s ease;
}
.scope-item:hover { border-color: var(--border-strong); background: #fff; }
.scope-item.checked {
  background: var(--accent-subtle);
  border-color: rgba(15, 108, 189, .35);
  box-shadow: inset 0 0 0 1px rgba(15, 108, 189, .12);
}
.si-top { display: flex; align-items: center; gap: 7px; }
.si-check {
  width: 16px; height: 16px; border-radius: 4px;
  display: inline-grid; place-items: center;
  border: 1px solid var(--border-strong); background: #fff;
  font-size: 11px; font-weight: 700; color: #fff; flex-shrink: 0;
}
.scope-item.checked .si-check {
  background: var(--accent); border-color: var(--accent);
}
.si-code { font-family: var(--mono); font-size: 12px; font-weight: 650; color: var(--text); }
.si-sens {
  margin-left: auto; font-size: 10px; font-weight: 600;
  color: #b45309; background: var(--warn-soft);
  padding: 1px 6px; border-radius: 4px;
}
.si-desc { font-size: 11.5px; color: var(--text-3); padding-left: 23px; line-height: 1.4; }

/* 新建结果框 */
.created-box {
  margin-top: 16px; padding: 14px 16px;
  background: var(--bg-soft); border: 1px solid rgba(15, 108, 189, .24);
  border-radius: 11px; display: flex; flex-direction: column; gap: 12px;
}
.cb-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 6px; }
.cb-title { font-size: 12.5px; font-weight: 650; color: var(--text); }
.cb-title.warn { color: var(--warn); }
.cb-token {
  display: block; padding: 9px 12px;
  background: #fff; border: 1px solid var(--border); border-radius: 8px;
  font-family: var(--mono); font-size: 12.5px; font-weight: 600; color: var(--text);
  word-break: break-all;
}
.cb-mcp { border-top: 1px solid var(--border); padding-top: 12px; }
.cb-endpoint { font-size: 11.5px; color: var(--text-3); margin-top: 2px; }
.cb-endpoint code { font-family: var(--mono); color: var(--text-2); }
.cb-pre {
  margin: 0; padding: 10px 12px;
  background: #0f172a; color: #e2e8f0;
  border-radius: 8px; font-family: var(--mono); font-size: 11.5px; overflow: auto;
}

/* 已签发令牌网格 */
.token-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
  gap: 14px;
}
@media (max-width: 640px) { .token-grid { grid-template-columns: 1fr; } }

.token-card {
  display: flex; flex-direction: column; gap: 12px;
  padding: 16px 18px;
  background: var(--card); border: 1px solid var(--border);
  border-radius: var(--r); box-shadow: var(--shadow-sm);
  transition: box-shadow .24s ease, border-color .22s ease, transform .24s ease;
}
.token-card:hover { box-shadow: var(--shadow); border-color: var(--border-strong); transform: translateY(-1px); }
.token-card.revoked { opacity: .62; }
.tc-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.tc-title-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.tc-icon { font-size: 15px; }
.tc-name { font-size: 15px; font-weight: 700; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tc-prefix {
  font-family: var(--mono); font-size: 12px; color: var(--text-3);
  background: var(--bg-soft); border: 1px solid var(--border);
  padding: 1px 7px; border-radius: 6px;
}
.tc-head-right { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }

.tc-scopes { display: flex; flex-wrap: wrap; gap: 6px; }
.tc-scope-chip {
  font-family: var(--mono); font-size: 11.5px; font-weight: 560;
  color: var(--text-2); background: var(--bg-soft);
  border: 1px solid var(--border); padding: 2px 8px; border-radius: 6px;
}
.tc-scope-chip.mcp {
  color: var(--accent-strong); background: var(--accent-soft);
  border-color: rgba(15, 108, 189, .15);
}

.tc-meta-grid {
  display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px;
  padding: 9px 12px; background: var(--bg-soft);
  border: 1px solid rgba(15, 23, 42, .05); border-radius: 9px;
}
.tc-meta-cell { display: flex; flex-direction: column; gap: 2px; }
.tc-meta-cell .k { font-size: 10.5px; color: var(--text-3); }
.tc-meta-cell .v { font-size: 12px; font-weight: 600; color: var(--text-2); font-variant-numeric: tabular-nums; }

/* 审计日志 */
.audit-list { display: flex; flex-direction: column; gap: 8px; }
.audit-row {
  padding: 10px 12px;
  background: var(--bg-subtle); border: 1px solid var(--border);
  border-radius: 9px; font-size: 12px;
}
.ar-top { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.ar-tool { font-family: var(--mono); font-weight: 650; color: var(--text); font-size: 12.5px; }
.ar-meta { color: var(--text-3); }
.ar-meta b { color: var(--text-2); }
.ar-time { margin-left: auto; color: var(--text-3); font-variant-numeric: tabular-nums; }
.ar-args { margin-top: 5px; font-family: var(--mono); font-size: 11.5px; color: var(--text-2); word-break: break-all; }
.ar-err { margin-top: 5px; color: var(--danger); font-size: 11.5px; word-break: break-all; }
</style>
