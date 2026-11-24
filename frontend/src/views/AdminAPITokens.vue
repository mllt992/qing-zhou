<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">API / MCP Token</h2>
        <p class="page-sub">给 AI 客户端（Remote MCP /mcp）、脚本或外部运维用的机器身份：可收窄 scope、可过期、可吊销。明文只在创建时展示一次。</p>
      </div>
    </div>

    <n-alert type="warning" :bordered="false" style="margin-bottom:16px;">
      不要把 <code>backup:write</code> 或 <code>mcp:settings</code> 发给不可信集成；任何 Token 均<b>不能</b>调用在线更新 / 回滚、OAuth 配置或令牌管理接口。
    </n-alert>

    <n-card size="small" style="margin-bottom:16px;">
      <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:10px;">
        <span style="font-size:12px;color:var(--text-3);">快速权限预设：</span>
        <n-button size="tiny" secondary @click="applyPreset('mcp-readonly')">AI 只读监控</n-button>
        <n-button size="tiny" secondary @click="applyPreset('mcp-ops')">AI 日常运维</n-button>
        <n-button size="tiny" type="primary" secondary @click="applyPreset('mcp-admin')">AI 全量运维（推荐）</n-button>
      </div>
      <div style="display:flex;gap:10px;align-items:flex-end;flex-wrap:wrap;">
        <div style="flex:1;min-width:160px;">
          <div style="font-size:12px;color:var(--text-3);margin-bottom:4px;">名称</div>
          <n-input v-model:value="form.name" placeholder="如 ai-ops / telegram-bot" />
        </div>
        <div style="flex:2;min-width:240px;">
          <div style="font-size:12px;color:var(--text-3);margin-bottom:4px;">Scopes</div>
          <n-select v-model:value="form.scopes" :options="scopeOpts" multiple placeholder="至少选一个" />
        </div>
        <div>
          <div style="font-size:12px;color:var(--text-3);margin-bottom:4px;">有效期</div>
          <n-select v-model:value="form.ttl" :options="ttlOpts" style="width:140px;" />
        </div>
        <n-button type="primary" :loading="creating" @click="createToken">创建</n-button>
      </div>
      <div v-if="createdPlain" style="margin-top:12px;padding:12px;background:var(--bg-soft);border-radius:8px;display:flex;flex-direction:column;gap:10px;">
        <div>
          <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px;">
            <span style="font-size:12px;font-weight:600;color:var(--warning);">请立即复制保存，关闭后无法再查看明文密钥</span>
            <n-button size="tiny" @click="copyPlain">复制 Token</n-button>
          </div>
          <div style="font-family:monospace;font-size:12px;word-break:break-all;">{{ createdPlain }}</div>
        </div>
        <div v-if="createdMcpConfigJson" style="border-top:1px solid var(--border);padding-top:10px;">
          <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px;">
            <span style="font-size:12px;font-weight:600;">Remote MCP 客户端配置（可直接粘贴到 Claude Code / Antigravity / Cursor 等）</span>
            <n-button size="tiny" type="primary" secondary @click="copyMcpConfig">复制 MCP JSON</n-button>
          </div>
          <div style="font-size:11.5px;color:var(--text-3);margin-bottom:6px;">端点地址：<code>{{ createdMcpUrl }}</code></div>
          <pre style="margin:0;padding:8px 10px;background:var(--bg);border:1px solid var(--border);border-radius:6px;font-family:ui-monospace,Consolas,monospace;font-size:11.5px;overflow:auto;">{{ createdMcpConfigJson }}</pre>
        </div>
      </div>
    </n-card>

    <n-spin :show="loading">
      <div v-if="tokens.length" class="card-grid">
        <div v-for="t in tokens" :key="t.id" class="list-card">
          <div class="lc-head">
            <span class="lc-title">{{ t.name }}</span>
            <n-tag :type="statusType(t)" size="tiny" :bordered="false">{{ statusLabel(t) }}</n-tag>
          </div>
          <div class="lc-meta">
            <span class="kv">前缀 <b style="font-family:monospace;">{{ t.prefix }}…</b></span>
            <span class="kv">创建 {{ fmtDateTime(t.created_at) }}</span>
          </div>
          <div class="lc-meta">
            <span class="kv">Scopes <b>{{ (t.scopes || []).join(', ') }}</b></span>
          </div>
          <div class="lc-meta">
            <span class="kv">过期 {{ t.expires_at ? fmtDateTime(t.expires_at) : '永不过期' }}</span>
            <span class="kv">最近使用 {{ t.last_used_at ? fmtDateTime(t.last_used_at) : '从未' }}</span>
          </div>
          <div class="lc-foot">
            <n-button v-if="!t.revoked_at" size="tiny" type="error" @click="revoke(t)">吊销</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无 API Token" style="padding:40px 0;" />
    </n-spin>

    <n-card size="small" style="margin-top:20px;" title="最近 MCP 调用审计日志">
      <template #header-extra>
        <n-button size="tiny" :loading="auditLoading" @click="loadAuditLogs">刷新</n-button>
      </template>
      <n-spin :show="auditLoading">
        <div v-if="auditLogs.length" style="display:flex;flex-direction:column;gap:8px;">
          <div v-for="log in auditLogs" :key="log.id" style="padding:8px 10px;border:1px solid var(--border);border-radius:8px;font-size:12px;">
            <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
              <n-tag :type="log.status === 'ok' ? 'success' : (log.status === 'denied' ? 'warning' : 'error')" size="tiny" :bordered="false">{{ log.status }}</n-tag>
              <b style="font-family:monospace;">{{ log.tool_name }}</b>
              <span style="color:var(--text-3);">令牌：{{ log.token_name || ('#' + log.token_id) }}</span>
              <span style="color:var(--text-3);">{{ log.duration_ms }} ms</span>
              <span style="color:var(--text-3);margin-left:auto;">{{ fmtDateTime(log.created_at) }}</span>
            </div>
            <div v-if="log.args_summary" style="margin-top:4px;color:var(--text-2);font-family:monospace;word-break:break-all;">{{ log.args_summary }}</div>
            <div v-if="log.error_msg" style="margin-top:4px;color:var(--error,#dc2626);word-break:break-all;">{{ log.error_msg }}</div>
          </div>
        </div>
        <n-empty v-else description="暂无 MCP 调用记录" style="padding:20px 0;" />
      </n-spin>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { NCard, NInput, NButton, NTag, NSpin, NSelect, NEmpty, NAlert, useMessage, useDialog } from 'naive-ui'
import { apiGet, apiList, apiPost, apiDelete } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

const message = useMessage()
const dialog = useDialog()
const tokens = ref<any[]>([])
const loading = ref(false)
const auditLogs = ref<any[]>([])
const auditLoading = ref(false)
const creating = ref(false)
const createdPlain = ref('')
const createdMcpUrl = ref('')
const createdMcpConfigJson = ref('')

const scopeOpts = [
  { label: 'mcp:read（MCP 只读：监控/流量/诊断）', value: 'mcp:read' },
  { label: 'mcp:nodes（MCP 节点：TLS/入站/节点/分组）', value: 'mcp:nodes' },
  { label: 'mcp:servers（MCP 服务器：SSH/重装/同步）', value: 'mcp:servers' },
  { label: 'mcp:users（MCP 用户与套餐运维）', value: 'mcp:users' },
  { label: 'mcp:settings（MCP 系统设置与 Telegram）', value: 'mcp:settings' },
  { label: 'stats:read（REST 统计只读）', value: 'stats:read' },
  { label: 'users:read（REST 用户只读）', value: 'users:read' },
  { label: 'nodes:read（REST 节点只读）', value: 'nodes:read' },
  { label: 'servers:read（REST 服务器只读）', value: 'servers:read' },
  { label: 'backup:write（REST 备份导出，敏感）', value: 'backup:write' },
]
const ttlOpts = [
  { label: '永不过期', value: 0 },
  { label: '7 天', value: 7 * 86400 },
  { label: '30 天', value: 30 * 86400 },
  { label: '90 天', value: 90 * 86400 },
  { label: '365 天', value: 365 * 86400 },
]
const form = reactive({ name: '', scopes: ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users', 'mcp:settings'] as string[], ttl: 0 })

function applyPreset(preset: 'mcp-readonly' | 'mcp-ops' | 'mcp-admin') {
  if (preset === 'mcp-readonly') {
    form.scopes = ['mcp:read']
    if (!form.name.trim()) form.name = 'ai-readonly'
  } else if (preset === 'mcp-ops') {
    form.scopes = ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users']
    if (!form.name.trim()) form.name = 'ai-ops'
  } else {
    form.scopes = ['mcp:read', 'mcp:nodes', 'mcp:servers', 'mcp:users', 'mcp:settings']
    if (!form.name.trim()) form.name = 'ai-admin'
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
  if (!form.name.trim()) { message.warning('请填写名称'); return }
  if (!form.scopes.length) { message.warning('请选择 scope'); return }
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
    const res = await apiGet<any>('/api/admin/mcp/audit-logs?limit=30')
    auditLogs.value = res?.rows || []
  } catch {} finally { auditLoading.value = false }
}

onMounted(() => { load(); loadAuditLogs() })
</script>
