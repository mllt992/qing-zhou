<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">证书管理</h2>
        <p class="page-sub">证书签发、导入、自动续期、指纹固定与节点 TLS 引用</p>
      </div>
      <div class="page-actions">
        <n-button @click="openSelf">生成自签证书</n-button>
        <n-button @click="openPaste">粘贴已有证书</n-button>
        <n-button type="primary" @click="openAcme()">申请真实证书（ACME）</n-button>
      </div>
    </div>

    <div class="resource-overview">
      <div class="resource-metric"><b>{{ certs.length }}</b><span>全部托管证书</span></div>
      <div class="resource-metric success"><b>{{ certs.filter(c => c.source === 'acme').length }}</b><span>ACME 真实证书</span></div>
      <div class="resource-metric"><b>{{ certs.filter(c => c.self_signed).length }}</b><span>自签证书</span></div>
      <div class="resource-metric" :class="{ warn: certs.filter(c => c.days_left >= 0 && c.days_left <= 30).length }">
        <b>{{ certs.filter(c => c.days_left >= 0 && c.days_left <= 30).length }}</b><span>30 天内到期</span>
      </div>
    </div>

    <div class="cert-banner">
      <div class="cb-icon">🛡️</div>
      <div class="cb-text">
        真实证书由面板本机签发与自动续期，可被任意节点的 TLS 配置引用。ACME 申请需先在
        <router-link to="/admin/settings">系统设置</router-link>
        填写 Cloudflare API Token；域名请在 Cloudflare 使用<b>灰云（DNS only）</b>指向节点 IP。需要引导式配置可使用
        <router-link to="/admin/setup">配置向导</router-link>。
      </div>
    </div>

    <n-spin :show="loading">
      <div v-if="certs.length" class="cert-grid">
        <div v-for="c in certs" :key="c.id" class="cert-card" :class="`st-${c.status}`">
          <!-- 顶部：名称 + 状态标签 -->
          <div class="cc-head">
            <div class="cc-title-wrap">
              <span class="cc-title" :title="c.name">{{ c.name }}</span>
              <div class="cc-badges">
                <n-tag :type="sourceType(c.source)" size="tiny" :bordered="false">{{ sourceLabel(c.source) }}</n-tag>
                <n-tag :type="statusType(c)" size="tiny" :bordered="false">{{ statusLabel(c) }}</n-tag>
              </div>
            </div>
            <div class="cc-domain-row">
              <code class="cc-domain">{{ c.domain || '—' }}</code>
              <n-button v-if="c.domain" text size="tiny" class="cc-copy" @click="copy(c.domain)">复制</n-button>
            </div>
          </div>

          <!-- 有效期进度与信息网格 -->
          <div class="cc-expiry-box">
            <div class="cc-expiry-top">
              <span class="cc-expiry-label">有效期至 <b>{{ c.not_after ? fmtDateTime(c.not_after) : '—' }}</b></span>
              <span v-if="c.not_after" class="cc-days" :class="daysClass(c.days_left)">
                {{ c.days_left < 0 ? '已过期' : `剩余 ${c.days_left} 天` }}
              </span>
            </div>
            <div v-if="c.not_after" class="cc-bar">
              <div class="cc-bar-fill" :class="daysClass(c.days_left)" :style="{ width: lifePercent(c) + '%' }" />
            </div>
          </div>

          <!-- 元数据规格表 -->
          <div class="cc-specs">
            <template v-if="c.source === 'acme'">
              <div class="cc-spec">
                <span class="k">验证方式</span>
                <span class="v">{{ methodLabel(c.acme_method) }}</span>
              </div>
              <div class="cc-spec">
                <span class="k">兼容策略</span>
                <span class="v">{{ profileLabel(c.acme_profile) }}</span>
              </div>
              <div class="cc-spec">
                <span class="k">实际密钥</span>
                <span class="v mono">{{ c.actual_key_type || '历史未记录' }}</span>
              </div>
              <div class="cc-spec">
                <span class="k">上次签发/续期</span>
                <span class="v">{{ c.last_renew_at ? fmtDateTime(c.last_renew_at) : '—' }}</span>
              </div>
            </template>
            <div v-if="c.actual_chain" class="cc-chain">
              <span class="k">已核验信任链</span>
              <span class="v mono">{{ c.actual_chain }}</span>
            </div>
          </div>

          <!-- 自签证书指纹区 -->
          <div v-if="c.self_signed && c.sha256" class="cc-pin-box">
            <div class="cc-pin-head">
              <span>证书指纹 SHA-256</span>
              <n-button text size="tiny" type="primary" @click="copyPin(c.sha256)">复制指纹</n-button>
            </div>
            <code class="cc-pin-code">{{ c.sha256 }}</code>
            <div class="cc-pin-tip">
              hysteria / hysteria2 订阅已自动附带 <code>pinSHA256</code>；手动配置其他客户端时可固定该指纹。
            </div>
          </div>

          <!-- 自动续期开关行 -->
          <div v-if="c.source === 'acme'" class="cc-autorenew">
            <div class="cc-ar-left">
              <span>到期前 30 天自动续期</span>
              <n-switch size="small" :value="c.auto_renew" @update:value="v => toggleAuto(c, v)" />
            </div>
            <span class="cc-ar-hint">{{ c.auto_renew ? '后台定时巡检' : '仅手动续期' }}</span>
          </div>

          <!-- 错误诊断折叠面板 -->
          <div v-if="c.last_error" class="cc-error-box">
            <div class="cc-error-head">
              <span class="cc-error-title">⚠️ 上次续期未成功</span>
              <span class="cc-error-tip">可点击下方「立即续期」或「重新申请」修复</span>
            </div>
            <div class="cc-error-msg">{{ c.last_error }}</div>
          </div>
          <div v-if="c.verify_error" class="cc-error-box">
            <div class="cc-error-head">
              <span class="cc-error-title">⚠️ 最近核验未通过（节点仍保留旧证书）</span>
            </div>
            <div class="cc-error-msg">{{ c.verify_error }}</div>
          </div>

          <!-- 操作底栏 -->
          <div class="cc-foot">
            <template v-if="c.source === 'acme'">
              <n-button size="small" type="primary" secondary :loading="renewingId === c.id" @click="renew(c)">
                立即续期
              </n-button>
              <n-button size="small" secondary @click="openAcme(c)">
                重新申请
              </n-button>
            </template>
            <n-button size="small" quaternary @click="exportCert(c)">查看 / 导出</n-button>
            <span style="flex:1;" />
            <n-button size="small" type="error" quaternary @click="del(c)">删除</n-button>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="暂无证书，点击右上角申请或导入" style="padding:48px 0;" />
    </n-spin>

    <!-- 申请 / 重新申请 ACME -->
    <n-modal v-model:show="showAcme" preset="card" :title="reissueCertId ? `重新申请证书 · ${acme.name}` : '申请真实证书（Let\'s Encrypt）'" style="max-width:540px;">
      <n-alert v-if="reissueCertId" type="info" :bordered="false" style="margin-bottom:14px;font-size:12.5px;">
        将重新调用 <code>acme.sh --issue</code> 签发并原地更新该证书（保留证书 ID 与所有节点的 TLS 绑定关系），签发成功后自动同步到关联节点。
      </n-alert>
      <n-form label-placement="left" label-width="90">
        <n-form-item label="名称"><n-input v-model:value="acme.name" placeholder="如 SSL通用证书" /></n-form-item>
        <n-form-item label="域名"><n-input v-model:value="acme.domain" placeholder="如 ssl.example.com 或 *.example.com" /></n-form-item>
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
            {label:'自动兼容（推荐，RSA 2048 + 优先 ISRG Root X1）',value:'auto'},
            {label:'最大兼容（固定 RSA 2048 + 优先 ISRG Root X1）',value:'compatible'},
            {label:'现代优先（ECDSA P-256 + CA 默认链）',value:'modern'},
          ]" />
        </n-form-item>
      </n-form>
      <div style="font-size:12px;color:var(--text-3);line-height:1.6;">
        Cloudflare Token 取自系统设置；签发后会核验私钥、域名、有效期和完整信任链，全部通过才会保存和下发。<b>DNS 验证通常需要 1–2 分钟</b>，点击后请耐心等待、勿关闭弹窗。
      </div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showAcme = false">取消</n-button>
          <n-button type="primary" :loading="submitting" @click="submitAcme">{{ reissueCertId ? '开始重新申请' : '立即申请' }}</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 粘贴证书 -->
    <n-modal v-model:show="showPaste" preset="card" title="粘贴已有证书" style="max-width:560px;">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="名称"><n-input v-model:value="paste.name" /></n-form-item>
        <n-form-item label="域名"><n-input v-model:value="paste.domain" placeholder="留空则自动取证书覆盖的名称（SAN）" /></n-form-item>
        <n-form-item label="证书 PEM"><n-input v-model:value="paste.certificate" type="textarea" :rows="4" placeholder="-----BEGIN CERTIFICATE-----（含完整链 fullchain）" /></n-form-item>
        <n-alert type="info" :show-icon="false" style="margin-bottom:8px;font-size:12px;">证书必须包含 SAN（subjectAltName）且覆盖所填域名；只有 CN 的旧式证书会被现代客户端拒绝，导入时会直接报错。</n-alert>
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
        <n-form-item label="附加 SAN"><n-input v-model:value="self.sans" placeholder="可选，逗号或空格分隔：节点 IPv4、IPv6、其它域名" /></n-form-item>
        <n-form-item label="有效天数"><n-input-number v-model:value="self.days" :min="1" :max="3650" style="width:160px;" /></n-form-item>
      </n-form>
      <div style="font-size:12px;color:var(--text-3);line-height:1.6;">自签证书会写入上面所有名称作为 SAN。HTTPS 代理客户端只按连接地址校验证书，按 IP 连接的节点请把它的 IPv4 / IPv6 都填进来。客户端需信任该证书、固定指纹或勾选「允许不安全」。</div>
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
            <n-input :value="exp.certificate" type="textarea" :rows="6" readonly style="font-family:var(--mono);font-size:11.5px;" />
            <n-button size="tiny" style="margin-top:6px;" @click="copy(exp.certificate)">复制证书</n-button>
          </div>
        </n-form-item>
        <n-form-item label="私钥 PEM">
          <div style="width:100%;">
            <n-input :value="exp.key" type="textarea" :rows="6" readonly style="font-family:var(--mono);font-size:11.5px;" />
            <n-button size="tiny" style="margin-top:6px;" @click="copy(exp.key)">复制私钥</n-button>
          </div>
        </n-form-item>
      </n-form>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { NButton, NTag, NSpin, NEmpty, NModal, NForm, NFormItem, NInput, NInputNumber, NSelect, NSwitch, NSpace, NAlert, useMessage, useDialog } from 'naive-ui'
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
  return { acme: 'ACME 真实证书', paste: '粘贴导入', selfsigned: '自签证书' }[s] || s
}
function profileLabel(s: string): string {
  return { auto: '自动兼容', compatible: '最大兼容', modern: '现代优先' }[s] || '旧版沿用'
}
function methodLabel(m: string): string {
  return { 'dns-cf': 'Cloudflare DNS', webroot: 'Webroot', 'http-01': 'HTTP-01' }[m] || 'Cloudflare DNS'
}
function daysClass(days: number): string {
  if (days <= 7) return 'crit'
  if (days <= 30) return 'warn'
  return 'ok'
}
function lifePercent(c: any): number {
  const total = c.source === 'acme' ? 90 : 365
  const left = Math.max(0, Number(c.days_left) || 0)
  return Math.max(4, Math.min(100, Math.round((left / total) * 100)))
}

async function copy(v: string) {
  if (await copyText(v)) message.success('已复制'); else message.error('复制失败，请手动复制')
}

// 申请 / 重新申请 ACME
const showAcme = ref(false)
const reissueCertId = ref(0)
const acme = reactive({ name: '', domain: '', method: 'dns-cf', webroot: '', acme_profile: 'auto' })
function openAcme(c?: any) {
  if (c && c.id) {
    reissueCertId.value = c.id
    acme.name = c.name || ''
    acme.domain = c.domain || ''
    acme.method = c.acme_method || 'dns-cf'
    acme.webroot = ''
    acme.acme_profile = c.acme_profile || (String(c.actual_key_type || '').startsWith('ECDSA') ? 'modern' : 'auto')
  } else {
    reissueCertId.value = 0
    acme.name = ''
    acme.domain = ''
    acme.method = 'dns-cf'
    acme.webroot = ''
    acme.acme_profile = 'auto'
  }
  showAcme.value = true
}
async function submitAcme() {
  if (!acme.name.trim() || !acme.domain.trim()) { message.error('名称和域名必填'); return }
  submitting.value = true
  try {
    const body = { name: acme.name, domain: acme.domain, method: acme.method, webroot: acme.webroot, acme_profile: acme.acme_profile }
    if (reissueCertId.value) {
      await apiPost(`/api/admin/certs/${reissueCertId.value}/reissue`, body, { timeoutMs: 5 * 60_000 })
      message.success('证书已重新申请并同步更新')
    } else {
      await apiPost('/api/admin/certs/acme', body, { timeoutMs: 5 * 60_000 })
      message.success('证书申请成功')
    }
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
const self = reactive({ name: '', server_name: '', sans: '', days: 3650 })
function openSelf() { self.name = ''; self.server_name = ''; self.sans = ''; self.days = 3650; showSelf.value = true }
async function submitSelf() {
  if (!self.name.trim() || !self.server_name.trim()) { message.error('名称和 SNI 必填'); return }
  submitting.value = true
  try {
    await apiPost('/api/admin/certs/self-signed', { name: self.name, server_name: self.server_name, days: self.days, sans: splitSans(self.sans) })
    message.success('已生成')
    showSelf.value = false
    await load()
  } catch (e: any) { message.error(e.message) } finally { submitting.value = false }
}

function splitSans(v: string): string[] {
  return (v || '').split(/[\s,，、]+/).map(x => x.trim()).filter(Boolean)
}

async function copyPin(v: string) {
  if (await copyText(v)) message.success('已复制证书指纹')
  else message.error('复制失败，请手动选中复制')
}

// 导出
const showExport = ref(false)
const exp = reactive({ name: '', certificate: '', key: '' })
async function exportCert(c: any) {
  try {
    const data = await apiGet<any>(`/api/admin/certs/${c.id}/export`)
    exp.name = c.name; exp.certificate = data?.certificate || ''; exp.key = data?.key || ''
    showExport.value = true
  } catch (e: any) { message.error(e.message) }
}

async function renew(c: any) {
  renewingId.value = c.id
  try {
    await apiPost(`/api/admin/certs/${c.id}/renew`, undefined, { timeoutMs: 5 * 60_000 })
    message.success('续期成功')
    await load()
  } catch (e: any) {
    message.error(e.message)
    await load()
  } finally { renewingId.value = 0 }
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

<style scoped>
.cert-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  margin-bottom: 16px;
  background: linear-gradient(135deg, rgba(15, 108, 189, .06), rgba(255, 255, 255, .9));
  border: 1px solid rgba(15, 108, 189, .14);
  border-radius: var(--r-sm);
  font-size: 12.5px;
  color: var(--text-2);
  line-height: 1.6;
}
.cb-icon { font-size: 18px; flex-shrink: 0; }
.cb-text a { font-weight: 600; }

.cert-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 16px;
}
@media (max-width: 640px) {
  .cert-grid { grid-template-columns: 1fr; }
}

.cert-card {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 18px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--r);
  box-shadow: var(--shadow-sm);
  transition: box-shadow .25s var(--ease-standard), border-color .22s var(--ease-standard), transform .25s var(--ease-emphasized);
}
.cert-card:hover {
  box-shadow: var(--shadow);
  border-color: var(--border-strong);
  transform: translateY(-2px);
}
.cert-card.st-expiring { border-top: 3px solid var(--warn); }
.cert-card.st-expired, .cert-card.st-decrypt_failed { border-top: 3px solid var(--danger); }
.cert-card.st-valid { border-top: 3px solid var(--success); }

.cc-head { display: flex; flex-direction: column; gap: 6px; }
.cc-title-wrap { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.cc-title { font-size: 15.5px; font-weight: 700; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cc-badges { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.cc-domain-row { display: flex; align-items: center; gap: 8px; }
.cc-domain {
  font-family: var(--mono);
  font-size: 13px;
  font-weight: 600;
  color: var(--accent-strong);
  background: var(--accent-soft);
  padding: 2px 8px;
  border-radius: 6px;
}

.cc-expiry-box {
  padding: 10px 12px;
  background: var(--bg-soft);
  border: 1px solid rgba(15, 23, 42, .06);
  border-radius: 10px;
}
.cc-expiry-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
}
.cc-expiry-label b { color: var(--text); font-variant-numeric: tabular-nums; }
.cc-days { font-weight: 700; font-size: 12px; font-variant-numeric: tabular-nums; }
.cc-days.ok { color: var(--success); }
.cc-days.warn { color: var(--warn); }
.cc-days.crit { color: var(--danger); }
.cc-bar {
  height: 5px;
  margin-top: 7px;
  border-radius: 999px;
  background: rgba(15, 23, 42, .08);
  overflow: hidden;
}
.cc-bar-fill { height: 100%; border-radius: inherit; transition: width .4s ease; }
.cc-bar-fill.ok { background: linear-gradient(90deg, #10b981, #059669); }
.cc-bar-fill.warn { background: linear-gradient(90deg, #f59e0b, #d97706); }
.cc-bar-fill.crit { background: linear-gradient(90deg, #ef4444, #dc2626); }

.cc-specs {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 12px;
  font-size: 12px;
}
.cc-spec {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.cc-spec .k, .cc-chain .k { color: var(--text-3); font-size: 11px; }
.cc-spec .v { color: var(--text); font-weight: 600; }
.cc-spec .v.mono, .cc-chain .v.mono { font-family: var(--mono); font-size: 11.5px; }
.cc-chain {
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-top: 6px;
  border-top: 1px dashed var(--border);
}
.cc-chain .v { color: var(--text-2); word-break: break-all; line-height: 1.45; }

.cc-pin-box {
  padding: 10px 12px;
  background: var(--bg-soft);
  border: 1px solid var(--border);
  border-radius: 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.cc-pin-head { display: flex; align-items: center; justify-content: space-between; font-size: 11.5px; color: var(--text-2); font-weight: 600; }
.cc-pin-code { font-family: var(--mono); font-size: 10.5px; color: var(--text-2); word-break: break-all; line-height: 1.45; }
.cc-pin-tip { font-size: 11px; color: var(--text-3); line-height: 1.45; }

.cc-autorenew {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 7px 11px;
  background: var(--bg-subtle);
  border: 1px solid rgba(15, 23, 42, .055);
  border-radius: 8px;
  font-size: 12px;
}
.cc-ar-left { display: flex; align-items: center; gap: 8px; font-weight: 560; color: var(--text-2); }
.cc-ar-hint { font-size: 11px; color: var(--text-3); }

.cc-error-box {
  padding: 10px 12px;
  background: var(--danger-soft);
  border: 1px solid rgba(220, 38, 38, .18);
  border-radius: 9px;
  font-size: 11.5px;
}
.cc-error-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 4px;
}
.cc-error-title { font-weight: 680; color: #b91c1c; }
.cc-error-tip { font-size: 11px; color: #991b1b; }
.cc-error-msg {
  font-family: var(--mono);
  font-size: 11px;
  color: #991b1b;
  word-break: break-all;
  line-height: 1.5;
  max-height: 84px;
  overflow-y: auto;
}

.cc-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: auto;
  padding-top: 10px;
  border-top: 1px solid var(--border);
}
</style>
