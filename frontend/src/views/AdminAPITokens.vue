<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">API Token</h2>
        <p class="page-sub">给脚本 / Telegram / 外部运维用的机器身份：可收窄 scope、可过期、可吊销。明文只在创建时展示一次。</p>
      </div>
    </div>

    <n-alert type="warning" :bordered="false" style="margin-bottom:16px;">
      不要把 <code>backup:write</code> 发给不可信集成；Token <b>不能</b>调用在线更新 / 回滚。管理 Token 本身只能用管理员登录会话。
    </n-alert>

    <n-card size="small" style="margin-bottom:16px;">
      <div style="display:flex;gap:10px;align-items:flex-end;flex-wrap:wrap;">
        <div style="flex:1;min-width:160px;">
          <div style="font-size:12px;color:var(--text-3);margin-bottom:4px;">名称</div>
          <n-input v-model:value="form.name" placeholder="如 telegram-bot" />
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
      <div v-if="createdPlain" style="margin-top:12px;padding:10px;background:var(--bg-soft);border-radius:8px;">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px;">
          <span style="font-size:12px;font-weight:600;color:var(--warning);">请立即复制，关闭后无法再查看明文</span>
          <n-button size="tiny" @click="copyPlain">复制</n-button>
        </div>
        <div style="font-family:monospace;font-size:12px;word-break:break-all;">{{ createdPlain }}</div>
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
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { NCard, NInput, NButton, NTag, NSpin, NSelect, NEmpty, NAlert, useMessage, useDialog } from 'naive-ui'
import { apiList, apiPost, apiDelete } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

const message = useMessage()
const dialog = useDialog()
const tokens = ref<any[]>([])
const loading = ref(false)
const creating = ref(false)
const createdPlain = ref('')

const scopeOpts = [
  { label: 'stats:read', value: 'stats:read' },
  { label: 'users:read', value: 'users:read' },
  { label: 'nodes:read', value: 'nodes:read' },
  { label: 'servers:read', value: 'servers:read' },
  { label: 'backup:write（敏感）', value: 'backup:write' },
]
const ttlOpts = [
  { label: '永不过期', value: 0 },
  { label: '7 天', value: 7 * 86400 },
  { label: '30 天', value: 30 * 86400 },
  { label: '90 天', value: 90 * 86400 },
  { label: '365 天', value: 365 * 86400 },
]
const form = reactive({ name: '', scopes: ['stats:read'] as string[], ttl: 0 })

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
  try {
    const data = await apiPost<any>('/api/admin/tokens', {
      name: form.name.trim(),
      scopes: form.scopes,
      expires_in: form.ttl || 0,
    })
    createdPlain.value = data.token
    form.name = ''
    message.success('已创建，请复制明文 Token')
    await load()
  } catch (e: any) { message.error(e.message) }
  finally { creating.value = false }
}

function copyPlain() {
  copyText(createdPlain.value)
  message.success('已复制')
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

onMounted(load)
</script>
