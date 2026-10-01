<template>
  <div>
    <div class="page-head"><div><h2 class="page-title">证书管理</h2><p class="page-sub">证书签发、导入、续期、指纹与节点 TLS 引用</p></div></div>
    <div class="resource-overview">
      <div class="resource-metric"><b>{{ certs.length }}</b><span>全部证书</span></div>
      <div class="resource-metric success"><b>{{ certs.filter(c => c.source === 'acme').length }}</b><span>ACME 真实证书</span></div>
      <div class="resource-metric"><b>{{ certs.filter(c => c.self_signed).length }}</b><span>自签证书</span></div>
      <div class="resource-metric" :class="{ warn: certs.filter(c => c.days_left >= 0 && c.days_left <= 30).length }"><b>{{ certs.filter(c => c.days_left >= 0 && c.days_left <= 30).length }}</b><span>30 天内到期</span></div>
    </div>

    <n-card size="small" style="margin-bottom:16px;">
      <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap;">
        <n-button type="primary" @click="openAcme">申请证书（Cloudflare DNS）</n-button>
        <n-button @click="openPaste">粘贴证书</n-button>
        <n-button @click="openSelf">生成自签证书</n-button>
        <span style="flex:1;"></span>
        <span style="font-size:12px;color:var(--text-3);">共 {{ certs.length }} 张 · 真实证书由面板本机签发/续期，可被任意节点的 TLS 配置引用</span>
      </div>
      <div style="margin-top:8px;font-size:12px;color:var(--text-3);">
        提示：ACME 申请需先在
        <router-link to="/admin/settings" style="color:var(--primary);">系统设置</router-link>
        填写 Cloudflare API Token；DNS 验证在面板本机完成，域名请在 Cloudflare 用<b>灰云（DNS only）</b>指向节点 IP 以保持直连速度。
      </div>
    </n-card>

    <n-spin :show="loading">
      <div v-if="certs.length" class="card-grid">
        <div v-for="c in certs" :key="c.id" class="list-card">
          <div class="lc-head">
            <span class="lc-title">{{ c.name }}</span>
            <n-tag :type="statusType(c)" size="tiny" :bordered="false">{{ statusLabel(c) }}</n-tag>
          </div>
          <div class="lc-meta">
            <span class="kv">域名 <b>{{ c.domain || '—' }}</b></span>
            <n-tag :type="sourceType(c.source)" size="tiny" :bordered="false">{{ sourceLabel(c.source) }}</n-tag>
          </div>
          <div class="lc-meta">
            <span class="kv">到期 <b>{{ c.not_after ? fmtDateTime(c.not_after) : '—' }}</b></span>
            <span v-if="c.not_after" class="kv">剩余 <b>{{ c.days_left }} 天</b></span>
          </div>
          <div v-if="c.source === 'acme'" class="lc-meta">
            <span class="kv">策略 <b>{{ profileLabel(c.acme_profile) }}</b></span>
            <span class="kv">实际密钥 <b>{{ c.actual_key_type || '历史证书未记录' }}</b></span>
          </div>
          <div v-if="c.actual_chain" class="lc-meta" style="font-size:11px;word-break:break-all;">
            已核验链：{{ c.actual_chain }}
          </div>
          <div class="lc-meta">
            <span class="kv">自动续期
              <n-switch size="small" :value="c.auto_renew" :disabled="c.source !== 'acme'" @update:value="v => toggleAuto(c, v)" />
            </span>
            <span v-if="c.last_renew_at" class="kv" style="color:var(--text-3);">上次续期 {{ fmtDateTime(c.last_renew_at) }}</span>
          </div>
          <div v-if="c.self_signed && c.sha256" class="lc-meta" style="flex-direction:column;align-items:flex-start;gap:2px;">
            <span class="kv" style="color:var(--text-3);">
              证书指纹 SHA-256
              <n-button text size="tiny" style="margin-left:6px;" @click="copyPin(c.sha256)">复制</n-button>
            </span>
            <code style="font-size:10px;word-break:break-all;line-height:1.4;color:var(--text-3);">{{ c.sha256 }}</code>
            <span style="font-size:11px;color:var(--text-3);">
              自签证书没有 CA 背书。hysteria / hysteria2 订阅链接已自动带上 <code>pinSHA256</code>；手工配置其他客户端时可粘贴此值，比「跳过证书校验」安全。
            </span>
          </div>
          <div v-if="c.last_error" class="lc-meta" style="color:var(--error-color, #d03050);font-size:11px;word-break:break-all;">
            续期错误：{{ c.last_error }}
          </div>
          <div v-if="c.verify_error" class="lc-meta" style="color:var(--error-color, #d03050);font-size:11px;word-break:break-all;">
            最近核验未通过（仍在使用旧证书）：{{ c.verify_error }}
          </div>
          <div class="lc-foot">
            <n-button v-if="c.source === 'acme'" size="tiny" :loading="renewingId === c.id" @click="renew(c)">立即续期</n-button>
            <n-button size="tiny" @click="exportCert(c)">查看 / 导出</n-button>
            <n-button size="tiny" type="error" @click="del(c)">删除</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无证书" style="padding:40px 0;" />
    </n-spin>

    <!-- 申请 ACME -->
    <n-modal v-model:show="showAcme" preset="card" title="申请真实证书（Let's Encrypt）" style="max-width:520px;">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="名称"><n-input v-model:value="acme.name" placeholder="如 trojan-cert" /></n-form-item>
        <n-form-item label="域名"><n-input v-model:value="acme.domain" placeholder="如 node.example.com（需已在 Cloudflare 解析）" /></n-form-item>
        <n-form-item label="验证方式">
          <n-select v-model:value="acme.method" :options="[
            {label:'Cloudflare DNS（推荐，无需端口，支持泛域名）',value:'dns-cf'},
            {label:'Webroot（网站根目录）',value:'webroot'},
            {label:'HTTP-01 standalone（需 80 端口空闲）',value:'http-01'},
          ]" />
        </n-form-item>
        <n-form-item v-if="acme.method === 'webroot'" label="网站根目录"><n-input v-model:value="acme.webroot" placeholder="/var/www/html" /></n-form-item>
        <n-form-item label="兼容策略">
          <n-select v-model:value="acme.acme_profile" :options="[
            {label:'自动兼容（推荐，规则可随版本安全演进）',value:'auto'},
            {label:'最大兼容（固定 RSA 2048 + 优先 ISRG Root X1）',value:'compatible'},
            {label:'现代优先（ECDSA P-256 + CA 默认链）',value:'modern'},
          ]" />
        </n-form-item>
      </n-form>
      <div style="font-size:12px;color:var(--text-3);">Cloudflare Token 取自系统设置；签发后会核验私钥、域名、有效期和完整信任链，全部通过才会保存和下发。自动兼容当前采用 RSA 2048 + 优先 X1 链；已有证书仍沿用原策略。<b>DNS 验证通常需要 1-2 分钟</b>，点「申请」后请耐心等待、勿关闭弹窗。</div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showAcme = false">取消</n-button>
          <n-button type="primary" :loading="submitting" @click="submitAcme">申请</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 粘贴证书 -->
    <n-modal v-model:show="showPaste" preset="card" title="粘贴已有证书" style="max-width:560px;">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="名称"><n-input v-model:value="paste.name" /></n-form-item>
        <n-form-item label="域名"><n-input v-model:value="paste.domain" placeholder="留空则自动取证书 CN" /></n-form-item>
        <n-form-item label="证书 PEM"><n-input v-model:value="paste.certificate" type="textarea" :rows="4" placeholder="-----BEGIN CERTIFICATE-----（含完整链 fullchain）" /></n-form-item>
        <n-form-item label="私钥 PEM"><n-input v-model:value="paste.key" type="textarea" :rows="4" placeholder="-----BEGIN PRIVATE KEY-----" /></n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showPaste = false">取消</n-button>
          <n-button type="primary" :loading="submitting" @click="submitPaste">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 自签 -->
    <n-modal v-model:show="showSelf" preset="card" title="生成自签证书" style="max-width:480px;">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="名称"><n-input v-model:value="self.name" /></n-form-item>
        <n-form-item label="SNI / 域名"><n-input v-model:value="self.server_name" placeholder="如 www.icloud.com 或节点 IP" /></n-form-item>
        <n-form-item label="有效天数"><n-input-number v-model:value="self.days" :min="1" :max="3650" style="width:160px;" /></n-form-item>
      </n-form>
      <div style="font-size:12px;color:var(--text-3);">自签证书用于 TUIC / Hysteria2 等允许跳过校验的客户端；客户端需勾选「允许不安全」。</div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showSelf = false">取消</n-button>
          <n-button type="primary" :loading="submitting" @click="submitSelf">生成</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 导出 / 查看 -->
    <n-modal v-model:show="showExport" preset="card" :title="'证书 · ' + (exp.name || '')" style="max-width:640px;">
      <n-form label-placement="top">
        <n-form-item label="证书 PEM（fullchain）">
          <div style="width:100%;">
            <n-input :value="exp.certificate" type="textarea" :rows="6" readonly style="font-family:monospace;font-size:11px;" />
            <n-button size="tiny" style="margin-top:4px;" @click="copy(exp.certificate)">复制证书</n-button>
          </div>
        </n-form-item>
        <n-form-item label="私钥 PEM">
          <div style="width:100%;">
            <n-input :value="exp.key" type="textarea" :rows="6" readonly style="font-family:monospace;font-size:11px;" />
            <n-button size="tiny" style="margin-top:4px;" @click="copy(exp.key)">复制私钥</n-button>
          </div>
        </n-form-item>
      </n-form>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { NCard, NButton, NTag, NSpin, NEmpty, NModal, NForm, NFormItem, NInput, NInputNumber, NSelect, NSwitch, NSpace, useMessage, useDialog } from 'naive-ui'
import { apiList, apiGet, apiPost, apiPut, apiDelete } from '@/api'
import { fmtDateTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

const message = useMessage()
const dialog = useDialog()
const certs = ref<any[]>([])
const loading = ref(false)
const submitting = ref(false)
const renewingId = ref(0)

function statusType(c: any): any {
  return { valid: 'success', expiring: 'warning', expired: 'error', decrypt_failed: 'error' }[c.status as string] || 'default'
}
function statusLabel(c: any): string {
  return { valid: '有效', expiring: '即将到期', expired: '已过期', decrypt_failed: '无法解密' }[c.status as string] || '未知'
}
function sourceType(s: string): any {
  return { acme: 'success', paste: 'info', selfsigned: 'warning' }[s] || 'default'
}
function sourceLabel(s: string): string {
  return { acme: 'ACME 真实证书', paste: '粘贴导入', selfsigned: '自签' }[s] || s
}
function profileLabel(s: string): string {
  return { auto: '自动兼容', compatible: '最大兼容', modern: '现代优先' }[s] || '旧版沿用'
}

async function copy(v: string) {
  if (await copyText(v)) message.success('已复制'); else message.error('复制失败，请手动复制')
}

// 申请 ACME
const showAcme = ref(false)
const acme = reactive({ name: '', domain: '', method: 'dns-cf', webroot: '', acme_profile: 'auto' })
function openAcme() { acme.name = ''; acme.domain = ''; acme.method = 'dns-cf'; acme.webroot = ''; acme.acme_profile = 'auto'; showAcme.value = true }
async function submitAcme() {
  if (!acme.name.trim() || !acme.domain.trim()) { message.error('名称和域名必填'); return }
  submitting.value = true
  try {
    await apiPost('/api/admin/certs/acme', { name: acme.name, domain: acme.domain, method: acme.method, webroot: acme.webroot, acme_profile: acme.acme_profile })
    message.success('证书申请成功')
    showAcme.value = false
    await load()
  } catch (e: any) { message.error(e.message) } finally { submitting.value = false }
}

// 粘贴
const showPaste = ref(false)
const paste = reactive({ name: '', domain: '', certificate: '', key: '' })
function openPaste() { paste.name = ''; paste.domain = ''; paste.certificate = ''; paste.key = ''; showPaste.value = true }
async function submitPaste() {
  if (!paste.name.trim() || !paste.certificate.trim() || !paste.key.trim()) { message.error('名称、证书和私钥必填'); return }
  submitting.value = true
  try {
    await apiPost('/api/admin/certs/paste', { ...paste })
    message.success('已保存')
    showPaste.value = false
    await load()
  } catch (e: any) { message.error(e.message) } finally { submitting.value = false }
}

// 自签
const showSelf = ref(false)
const self = reactive({ name: '', server_name: '', days: 3650 })
function openSelf() { self.name = ''; self.server_name = ''; self.days = 3650; showSelf.value = true }
async function submitSelf() {
  if (!self.name.trim() || !self.server_name.trim()) { message.error('名称和 SNI 必填'); return }
  submitting.value = true
  try {
    await apiPost('/api/admin/certs/self-signed', { ...self })
    message.success('已生成')
    showSelf.value = false
    await load()
  } catch (e: any) { message.error(e.message) } finally { submitting.value = false }
}

async function copyPin(v: string) {
  try {
    await navigator.clipboard.writeText(v)
    message.success('已复制证书指纹')
  } catch { message.error('复制失败，请手动选中复制') }
}

// 导出
const showExport = ref(false)
const exp = reactive({ name: '', certificate: '', key: '' })
async function exportCert(c: any) {
  try {
    const data = await apiPost<any>(`/api/admin/certs/${c.id}/export`)
    exp.name = c.name; exp.certificate = data?.certificate || ''; exp.key = data?.key || ''
    showExport.value = true
  } catch (e: any) { message.error(e.message) }
}

async function renew(c: any) {
  renewingId.value = c.id
  try {
    await apiPost(`/api/admin/certs/${c.id}/renew`)
    message.success('续期成功')
    await load()
  } catch (e: any) { message.error(e.message) } finally { renewingId.value = 0 }
}

async function toggleAuto(c: any, v: boolean) {
  try { await apiPut(`/api/admin/certs/${c.id}`, { auto_renew: v }); c.auto_renew = v; message.success(v ? '已开启自动续期' : '已关闭自动续期') }
  catch (e: any) { message.error(e.message) }
}

function del(c: any) {
  dialog.warning({
    title: '删除证书',
    content: `确定删除「${c.name}」？若仍被 TLS 配置引用将拒绝删除。`,
    positiveText: '删除', negativeText: '取消',
    onPositiveClick: async () => {
      try { await apiDelete(`/api/admin/certs/${c.id}`); message.success('已删除'); await load() }
      catch (e: any) { message.error(e.message) }
    },
  })
}

async function load() {
  loading.value = true
  try { certs.value = await apiList('/api/admin/certs') } catch {} finally { loading.value = false }
}
onMounted(load)
</script>
