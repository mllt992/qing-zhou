<template>
  <div class="setup-page">
    <div class="setup-head">
      <h2 class="page-title">配置向导</h2>
      <p class="page-sub">按步骤完成：服务器 → 域名 → 证书 → TLS → 入站 → 验证。随时可以新建，也可以从服务器 / 入站列表进入编辑。</p>
    </div>

    <n-spin :show="loading">
      <n-card size="small" class="setup-card">
        <div class="mode-bar">
          <n-radio-group :value="editingId ? 'edit' : 'create'" size="small" @update:value="onModeToggle">
            <n-radio-button value="create">新建配置</n-radio-button>
            <n-radio-button value="edit" :disabled="!editableInboundOpts.length">编辑已有入站</n-radio-button>
          </n-radio-group>
          <n-select
            v-if="showEditPicker || editingId"
            :value="editingId || null"
            :options="editableInboundOpts"
            placeholder="选择要编辑的入站"
            size="small"
            filterable
            style="max-width:340px;flex:1"
            @update:value="pickInboundToEdit"
          />
        </div>

        <n-alert v-if="editingId" type="info" :show-icon="false" class="setup-edit-tip">
          正在<b>编辑</b>入站「{{ tag }}」：已填入现有配置，只改你需要的步骤；高级参数与中转/出口绑定会原样保留。
        </n-alert>

        <n-steps :current="idx + 1" size="small" class="setup-steps" @update:current="goto">
          <n-step v-for="k in steps" :key="k" :title="STEP_TITLES[k]" />
        </n-steps>

        <!-- ① 用途 -->
        <section v-if="cur === 'goal'" class="setup-body" aria-label="选择用途">
          <h3 class="sec-title">你想配置哪种服务？</h3>
          <div class="goal-grid" role="radiogroup" aria-label="协议场景">
            <button v-for="s in SCENARIOS" :key="s.key" type="button" role="radio" :aria-checked="s.key === scenarioKey"
              class="goal-card" :class="{ active: s.key === scenarioKey }" :disabled="!!editingId && s.key !== scenarioKey"
              @click="pickScenario(s.key)">
              <span class="goal-title">{{ s.title }}</span>
              <span class="goal-need">{{ needText(s) }}</span>
              <span class="goal-desc">{{ s.desc }}</span>
            </button>
          </div>
          <div v-if="canChooseCertMode(scenario)" class="setup-row">
            <n-checkbox v-model:checked="useRealCert">使用域名 + 真实证书（更规范；不勾选则一键自签，客户端需跳过校验或固定指纹）</n-checkbox>
          </div>
          <n-alert v-if="scenario.cert === 'optional'" type="info" :show-icon="false" class="setup-gap">
            {{ scenario.title }} 的客户端通常<b>不校验证书</b>，不想折腾域名就保持默认的「一键自签」。
          </n-alert>
        </section>

        <!-- ② 服务器 -->
        <section v-else-if="cur === 'server'" class="setup-body" aria-label="选择服务器">
          <h3 class="sec-title">在哪台服务器上配置？</h3>
          <n-select v-model:value="serverId" :options="serverOpts" placeholder="选择服务器" filterable style="max-width:420px" @update:value="onServerChange" />
          <n-alert v-if="!servers.length && !loading" type="warning" :show-icon="false" class="setup-gap">
            还没有服务器。请先到
            <n-button text type="primary" @click="router.push('/admin/servers')">服务器管理</n-button>
            添加，然后回到这里。
          </n-alert>
          <p v-if="server" class="setup-note">
            SSH 地址 <code>{{ server.host }}</code>
            <template v-if="server.host_v6"> · IPv6 <code>{{ server.host_v6 }}</code></template>
            <template v-if="server.domain"> · 已绑定域名 <code>{{ server.domain }}</code></template>
          </p>
          <p class="setup-note">面板所在的本机节点暂不在向导内，请继续使用 sing-box 页面配置。</p>
        </section>

        <!-- ③ 域名 -->
        <section v-else-if="cur === 'domain'" class="setup-body" aria-label="域名与解析">
          <h3 class="sec-title">给 {{ server?.name }} 设置域名</h3>
          <n-alert v-if="server?.domain" type="success" :show-icon="false" class="setup-gap-b">
            当前已绑定域名 <b>{{ server.domain }}</b>。想换域名就在下面重新填写保存；不换直接点「下一步」。
          </n-alert>
          <div class="setup-row">
            <template v-if="cf.configured && zones.length">
              <n-input v-model:value="prefix" placeholder="子域名前缀，如 vps-taxi" style="max-width:220px" @update:value="syncDomain" />
              <span class="dot">.</span>
              <n-select v-model:value="zone" :options="zoneOpts" placeholder="根域名" style="width:240px" @update:value="syncDomain" />
            </template>
            <n-input v-else v-model:value="domain" placeholder="完整域名，如 vps-taxi.example.com" style="max-width:420px" />
          </div>
          <p class="setup-note" v-if="domain">将指向 <code>{{ domain }}</code> → <code>{{ server?.host }}</code></p>
          <n-alert v-if="!cf.configured" type="warning" :show-icon="false" class="setup-gap">
            还没配置 Cloudflare API Token，无法自动创建解析。可以到
            <n-button text type="primary" @click="router.push('/admin/settings')">系统设置</n-button>
            填写后回来，或在你的域名服务商处<b>手动</b>添加一条 A 记录指向 <code>{{ server?.host }}</code>，然后点「我已自行解析，仅保存域名」。
          </n-alert>
          <div v-if="cf.configured && server?.host_v6" class="setup-row">
            <n-checkbox v-model:checked="syncV6">同时写入 AAAA 记录（指向 {{ server.host_v6 }}）</n-checkbox>
          </div>
          <n-space class="setup-gap">
            <n-button v-if="cf.configured" type="primary" :loading="busy.dns" :disabled="!domain" @click="saveDomain('cf')">写入 Cloudflare 解析并保存</n-button>
            <n-button :loading="busy.dns" :disabled="!domain" @click="saveDomain('manual')">我已自行解析，仅保存域名</n-button>
            <n-button :loading="busy.check" :disabled="!effDomain" @click="checkDns">检测解析</n-button>
          </n-space>
          <n-alert v-if="dnsRes" :type="dnsAlertType" :show-icon="false" class="setup-gap">
            <template v-if="dnsRes.status === 'ok'">解析正常：{{ dnsRes.domain }} → {{ dnsRes.resolved.join('、') }}。</template>
            <template v-else-if="dnsRes.status === 'mismatch'">解析到了 {{ dnsRes.resolved.join('、') }}，不是这台服务器（{{ dnsRes.expected_v4 }}）。请检查记录；若刚改过，可能还在生效。</template>
            <template v-else>暂时解析不到。刚添加的记录通常几分钟内生效，稍后再点「检测解析」。</template>
            <template v-if="dnsRes.expected_v6 && !dnsRes.v6_ok"><br />IPv6 未解析到 {{ dnsRes.expected_v6 }}：只是提醒，不影响使用（证书一般只覆盖域名 / IPv4）。</template>
          </n-alert>
        </section>

        <!-- ④ 证书 -->
        <section v-else-if="cur === 'cert'" class="setup-body" aria-label="证书">
          <h3 class="sec-title">{{ effDomain }} 的证书</h3>
          <n-alert v-if="!effDomain" type="warning" :show-icon="false">请先回到上一步保存域名。</n-alert>
          <template v-else>
            <template v-if="candidates.length">
              <p class="setup-note">证书中心里已有可覆盖该域名的证书，直接选用即可：</p>
              <n-radio-group v-model:value="certId" class="radio-col">
                <n-radio v-for="c in candidates" :key="c.id" :value="c.id">
                  {{ c.name }}（{{ c.domain }}，{{ c.days_left >= 0 ? '剩余 ' + c.days_left + ' 天' : '到期未知' }}{{ c.self_signed ? '，自签' : '' }}）
                </n-radio>
              </n-radio-group>
              <n-divider>或者申请新证书</n-divider>
            </template>
            <div class="acme-box">
              <p class="setup-note">
                申请由<b>面板所在机器</b>运行 acme.sh 完成（Let's Encrypt）。状态：
                <n-tag size="small" :type="pre.acme_installed ? 'success' : 'warning'" :bordered="false">{{ pre.acme_installed ? 'acme.sh 已安装' : 'acme.sh 未安装' }}</n-tag>
                <n-tag size="small" :type="cf.configured ? 'success' : 'default'" :bordered="false">{{ cf.configured ? 'Cloudflare Token 已配置' : '未配置 Cloudflare Token' }}</n-tag>
              </p>
              <div class="setup-row">
                <n-input v-model:value="acmeEmail" placeholder="ACME 账号邮箱（可选，用于到期提醒）" style="max-width:320px" />
                <n-button v-if="!pre.acme_installed" :loading="busy.install" @click="installAcme">一键安装 acme.sh</n-button>
              </div>
              <div class="setup-row">
                <n-radio-group v-model:value="acmeMethod">
                  <n-radio value="dns-cf" :disabled="!cf.configured">DNS 验证（Cloudflare，推荐，无需开放端口）</n-radio>
                  <n-radio value="http-01">HTTP 验证（需面板机器 80 端口可从公网访问，且域名解析到面板机器）</n-radio>
                </n-radio-group>
              </div>
              <n-button type="primary" :loading="busy.issue" :disabled="!pre.acme_installed || (acmeMethod === 'dns-cf' && !cf.configured)" @click="issueCert">
                {{ busy.issue ? '申请中，最长约 3～4 分钟，请勿关闭页面…' : '申请 ' + effDomain + ' 的证书' }}
              </n-button>
            </div>
            <p class="setup-note">已有证书文件？到 <n-button text type="primary" @click="router.push('/admin/certs')">证书管理</n-button> 粘贴导入，再回来选用。</p>
          </template>
        </section>

        <!-- ⑤ TLS -->
        <section v-else-if="cur === 'tls'" class="setup-body" aria-label="TLS 配置">
          <h3 class="sec-title">TLS 配置</h3>
          <p class="setup-note">名称与 SNI 已按域名自动填好（SNI = <code>{{ effDomain }}</code>），无需手动选择。</p>
          <n-alert v-if="tlsId" type="success" :show-icon="false">已就绪：TLS 配置「{{ tlsName }}」。</n-alert>
          <n-space class="setup-gap">
            <n-button type="primary" :loading="busy.tls" :disabled="!certId" @click="ensureTls">{{ tlsId ? '重新创建 / 复用' : '创建 TLS 配置' }}</n-button>
          </n-space>
        </section>

        <!-- 自签 -->
        <section v-else-if="cur === 'selfsigned'" class="setup-body" aria-label="自签证书">
          <h3 class="sec-title">自签证书</h3>
          <n-alert :type="scenario.cert === 'required' ? 'warning' : 'info'" :show-icon="false" class="setup-gap-b">
            自签证书不受系统信任：客户端需勾选「跳过证书验证」或固定证书指纹。
            <template v-if="scenario.cert === 'required'">1Panel、Docker、curl、Git 等标准 HTTPS 代理客户端默认会直接拒连，建议改用域名 + 真实证书。</template>
            <template v-else>{{ scenario.title }} 的客户端通常本来就不校验。</template>
          </n-alert>
          <n-radio-group v-model:value="selfMode" class="radio-col">
            <n-radio value="new">按服务器地址一键新签一张（推荐）</n-radio>
            <n-radio value="reuse" :disabled="!reusableTls.length">复用已有 TLS 配置</n-radio>
          </n-radio-group>
          <n-select v-if="selfMode === 'reuse'" v-model:value="tlsId" :options="reusableTls" placeholder="选择已有 TLS 配置" style="max-width:420px;margin-top:8px" />
          <n-space class="setup-gap">
            <n-button v-if="selfMode === 'new'" type="primary" :loading="busy.tls" @click="quickSelfSigned">{{ tlsId ? '重新生成' : '生成自签证书' }}</n-button>
          </n-space>
          <n-alert v-if="tlsId" type="success" :show-icon="false" class="setup-gap">已就绪：TLS 配置「{{ tlsName }}」。</n-alert>
        </section>

        <!-- Reality -->
        <section v-else-if="cur === 'reality'" class="setup-body" aria-label="Reality 参数">
          <h3 class="sec-title">Reality 参数</h3>
          <p class="setup-note">选一个真实存在、支持 TLS 1.3 的网站作为伪装目标，密钥和 Short ID 自动生成。</p>
          <n-input v-model:value="realitySni" placeholder="如 www.microsoft.com" style="max-width:360px" />
          <n-space class="setup-gap">
            <n-button type="primary" :loading="busy.tls" :disabled="!realitySni.trim()" @click="createReality">{{ tlsId ? '重新生成 Reality 配置' : '生成 Reality 配置' }}</n-button>
          </n-space>
          <n-alert v-if="tlsId" type="success" :show-icon="false" class="setup-gap">已就绪：TLS 配置「{{ tlsName }}」。</n-alert>
        </section>

        <!-- ⑥ 入站 -->
        <section v-else-if="cur === 'inbound'" class="setup-body" aria-label="创建入站">
          <h3 class="sec-title">{{ editingId ? '保存入站' : '创建入站' }}</h3>
          <div class="form-grid">
            <label>名称（tag）<n-input v-model:value="tag" placeholder="如 https-proxy" /></label>
            <label>端口<n-input-number v-model:value="port" :min="1" :max="65535" :show-button="false" /></label>
          </div>
          <p class="setup-note">
            协议 <b>{{ scenario.title }}</b> · 服务器 <b>{{ server?.name }}</b>
            <template v-if="tlsId"> · TLS「{{ tlsName }}」</template>
          </p>
          <n-alert v-if="isQuic" type="info" :show-icon="false" class="setup-gap">该协议使用 <b>UDP</b>：请在云厂商安全组 / 防火墙放行 UDP {{ port }}。</n-alert>
          <n-alert v-else type="info" :show-icon="false" class="setup-gap">请确认安全组 / 防火墙已放行 TCP {{ port }}。</n-alert>
        </section>

        <!-- ⑦ 验证 -->
        <section v-else-if="cur === 'verify'" class="setup-body" aria-label="验证">
          <h3 class="sec-title">验证</h3>
          <n-alert v-if="!savedId" type="warning" :show-icon="false">入站还没有保存，请回到上一步。</n-alert>
          <template v-else>
            <p class="setup-note">入站已保存，配置正在推送到服务器（通常几秒）。</p>
            <n-spin :show="busy.verify">
              <div class="verify-box">
                <template v-if="verify">
                  <p>
                    <n-tag :type="verdict.type" size="small" :bordered="false">TLS {{ verdict.text }}</n-tag>
                  </p>
                  <div v-for="(r, i) in verify.results || []" :key="i" class="verify-row">
                    <b>{{ r.addr }}</b>
                    <n-tag size="tiny" :type="verifyVerdict(r.status).type" :bordered="false">{{ tlsStatusText(r.status) }}</n-tag>
                    <ul>
                      <li v-for="(m, j) in r.problems || []" :key="'p' + j" class="v-problem">{{ m }}</li>
                      <li v-for="(m, j) in r.warnings || []" :key="'w' + j" class="v-warn">{{ m }}</li>
                      <li v-for="(m, j) in r.notes || []" :key="'n' + j" class="v-note">{{ m }}</li>
                    </ul>
                  </div>
                </template>
                <p v-else-if="!busy.verify" class="setup-note">{{ verifyHint }}</p>
              </div>
            </n-spin>
            <n-alert type="info" :show-icon="false" class="setup-gap">
              「证书不覆盖某个地址」只是提醒（例如证书只覆盖域名 / IPv4、不覆盖 IPv6），不影响按域名连接。
            </n-alert>
            <n-space class="setup-gap">
              <n-button :loading="busy.verify" @click="runVerify(true)">重新检测</n-button>
              <n-button @click="router.push('/admin/nodes')">去节点管理绑定</n-button>
              <n-button @click="router.push('/admin/singbox')">去 sing-box 查看</n-button>
              <n-button type="primary" @click="restart">再配一个</n-button>
            </n-space>
            <p class="setup-note">连接地址：<code>{{ connectHost }}:{{ port }}</code>（账号 / 凭据在「节点管理」和订阅里分配）</p>
          </template>
        </section>

        <div v-if="cur !== 'verify'" class="setup-nav">
          <n-button :disabled="idx <= 0" @click="prev">上一步</n-button>
          <n-button v-if="cur !== 'inbound'" type="primary" :disabled="!canNext" @click="next">下一步</n-button>
          <n-button v-else type="primary" :loading="busy.save" :disabled="!canNext" @click="saveInbound">{{ editingId ? '保存并验证' : '创建并验证' }}</n-button>
        </div>
      </n-card>
    </n-spin>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NCard, NSteps, NStep, NButton, NSelect, NInput, NInputNumber, NAlert, NTag, NRadioGroup, NRadio, NRadioButton,
  NSpace, NCheckbox, NSpin, NDivider, useMessage, useDialog,
} from 'naive-ui'
import { apiList, apiGet, apiPost, apiPut } from '@/api'
import {
  SCENARIOS, STEP_TITLES, QUIC_TYPES, scenarioByKey, scenarioForInbound, visibleSteps, defaultUseRealCert,
  canChooseCertMode, matchingCerts, inboundOptions, joinDomain, splitDomain, verifyVerdict, type Scenario, type StepKey,
} from '@/utils/setupWizard'

const route = useRoute(), router = useRouter(), message = useMessage(), dialog = useDialog()

const loading = ref(true)
const servers = ref<any[]>([]), certs = ref<any[]>([]), tlsList = ref<any[]>([]), inbounds = ref<any[]>([]), zones = ref<any[]>([])
const cf = reactive({ configured: false })
const pre = reactive({ acme_installed: false, acme_email: '' })
const busy = reactive({ dns: false, check: false, install: false, issue: false, tls: false, save: false, verify: false })

// 用途 / 步骤
const scenarioKey = ref('https-proxy')
const scenario = computed<Scenario>(() => scenarioByKey(scenarioKey.value))
const useRealCert = ref(true)
const steps = computed<StepKey[]>(() => visibleSteps(scenario.value, useRealCert.value))
const cur = ref<StepKey>('goal')
const idx = computed(() => Math.max(0, steps.value.indexOf(cur.value)))
const reached = ref(0)
const editingId = ref(0)
const showEditPicker = ref(false)
watch(steps, s => { if (!s.includes(cur.value)) cur.value = s[Math.min(1, s.length - 1)] })

// 表单状态
const serverId = ref<number | null>(null)
const domain = ref(''), prefix = ref(''), zone = ref('')
const syncV6 = ref(false)
const certId = ref(0), tlsId = ref(0)
const tag = ref(''), port = ref(443), listen = ref('::'), enabled = ref(true)
const existingOptions = ref(''), existingUpstreamId = ref(0), existingEgressId = ref(0)
const acmeEmail = ref(''), acmeMethod = ref('dns-cf')
const selfMode = ref<'new' | 'reuse'>('new')
const realitySni = ref('www.microsoft.com')
const dnsRes = ref<any>(null)
const savedId = ref(0)
const verify = ref<any>(null)
const verifyHint = ref('')

const server = computed(() => servers.value.find(s => s.id === serverId.value))
const effDomain = computed(() => (server.value?.domain || '').trim().toLowerCase())
const candidates = computed(() => matchingCerts(certs.value, effDomain.value))
const tlsName = computed(() => tlsList.value.find(t => t.id === tlsId.value)?.name || '')
const isQuic = computed(() => QUIC_TYPES.includes(scenario.value.type))
const connectHost = computed(() => server.value?.domain || server.value?.host || '')
const verdict = computed(() => verifyVerdict(verify.value?.status || 'skipped'))
const dnsAlertType = computed(() => dnsRes.value?.status === 'ok' ? 'success' : 'warning')
const serverOpts = computed(() => servers.value.map(s => ({ label: `${s.name}（${s.domain || s.host}）`, value: s.id })))
const zoneOpts = computed(() => zones.value.map((z: any) => ({ label: z.name, value: z.name })))
const editableInboundOpts = computed(() => inbounds.value
  .filter(ib => ib.server_id && scenarioForInbound(ib, tlsList.value.find(t => t.id === ib.tls_id) || null))
  .map(ib => {
    const sv = servers.value.find(s => s.id === ib.server_id)
    return { label: `${ib.tag}（${ib.type} :${ib.listen_port}${sv ? ' · ' + sv.name : ''}）`, value: ib.id }
  }))
const reusableTls = computed(() => tlsList.value
  .filter(t => t.mode !== 'reality' && !(t.server_json || '').includes('"reality"'))
  .map(t => ({ label: t.name, value: t.id })))

function needText(s: Scenario) {
  return ({ required: '需要域名 + 证书', recommended: '建议域名 + 证书', optional: '证书可选', none: '无需证书' } as Record<string, string>)[s.cert]
}
function tlsStatusText(s: string) {
  return ({ ok: '正常', warning: '提醒', error: '异常', skipped: '已跳过' } as Record<string, string>)[s] || s
}
function errMsg(e: any, fallback: string) { return (e && e.message) || fallback }

// 导航
const canNext = computed(() => {
  switch (cur.value) {
    case 'server': return serverId.value != null
    case 'domain': return !!effDomain.value
    case 'cert': return !!certId.value
    case 'tls': case 'selfsigned': case 'reality': return !!tlsId.value
    case 'inbound': return !!tag.value.trim() && port.value > 0
    default: return true
  }
})
function goto(n: number) {
  const k = steps.value[n - 1]
  if (!k) return
  if (editingId.value || n - 1 <= reached.value) cur.value = k
}
function next() {
  const n = Math.min(idx.value + 1, steps.value.length - 1)
  cur.value = steps.value[n]
  reached.value = Math.max(reached.value, n)
}
function prev() { cur.value = steps.value[Math.max(idx.value - 1, 0)] }

function applyScenarioDefaults(s: Scenario) {
  tag.value = s.tag; port.value = s.port
  useRealCert.value = defaultUseRealCert(s)
}
function pickScenario(key: string) {
  if (editingId.value) return
  scenarioKey.value = key
  applyScenarioDefaults(scenarioByKey(key))
  tlsId.value = 0
}
function onServerChange() {
  const s = server.value
  domain.value = s?.domain || ''
  const sp = splitDomain(domain.value, zones.value.map((z: any) => z.name))
  prefix.value = sp.prefix; zone.value = sp.zone
  dnsRes.value = null
}
function syncDomain() { const d = joinDomain(prefix.value, zone.value); if (d) domain.value = d }

// 域名 + 解析
async function saveDomain(mode: 'cf' | 'manual', overwrite = false) {
  if (!server.value || !domain.value.trim()) return
  busy.dns = true
  try {
    await apiPost(`/api/admin/servers/${server.value.id}/dns`, {
      domain: domain.value.trim(), domain_v6: server.value.domain_v6 || '',
      sync_v4: mode === 'cf', sync_v6: mode === 'cf' && syncV6.value, overwrite, cleanup_old: true,
    })
    message.success(mode === 'cf' ? '解析已写入并保存域名' : '域名已保存')
    await loadServers()
    await checkDns()
  } catch (e: any) {
    if (!overwrite && e?.status === 409) {
      dialog.warning({
        title: 'Cloudflare 已存在同名解析记录',
        content: `${errMsg(e, '')}\n\n确认要把该记录改为指向本服务器吗？`,
        positiveText: '确认覆盖', negativeText: '取消',
        onPositiveClick: () => saveDomain(mode, true),
      })
    } else message.error(errMsg(e, '保存失败'))
  } finally { busy.dns = false }
}
async function checkDns() {
  if (!effDomain.value) return
  busy.check = true
  try { dnsRes.value = await apiPost('/api/admin/setup/dns-check', { domain: effDomain.value, server_id: serverId.value || 0 }) }
  catch (e: any) { message.error(errMsg(e, '检测失败')) } finally { busy.check = false }
}

// 证书
async function installAcme() {
  busy.install = true
  try {
    const r: any = await apiPost('/api/admin/setup/acme/install', { email: acmeEmail.value.trim() }, { timeoutMs: 4 * 60_000 })
    pre.acme_installed = !!r?.acme_installed
    message.success('acme.sh 已安装')
  } catch (e: any) { message.error(errMsg(e, '安装失败')) } finally { busy.install = false }
}
async function issueCert() {
  if (!effDomain.value) return
  busy.issue = true
  try {
    const c: any = await apiPost('/api/admin/certs/acme',
      { name: effDomain.value, domain: effDomain.value, method: acmeMethod.value, webroot: '', acme_profile: 'auto' },
      { timeoutMs: 5 * 60_000 })
    message.success('证书申请成功')
    certs.value = await apiList('/api/admin/certs')
    if (c?.id) certId.value = c.id
  } catch (e: any) { message.error(errMsg(e, '证书申请失败')) } finally { busy.issue = false }
}

// TLS 配置
async function refreshTls() { tlsList.value = await apiList('/api/admin/sb/tls') }
async function ensureTls() {
  if (!certId.value || !effDomain.value) return
  busy.tls = true
  try {
    const exist = tlsList.value.find(t => t.cert_id === certId.value && t.mode !== 'reality')
    if (exist) { tlsId.value = exist.id; message.success('已复用现有 TLS 配置'); return }
    const t: any = await apiPost('/api/admin/sb/tls/cert', { name: effDomain.value, server_id: 0, server_name: effDomain.value, cert_id: certId.value, insecure: false })
    await refreshTls()
    tlsId.value = t.id
    message.success('TLS 配置已创建')
  } catch (e: any) { message.error(errMsg(e, '创建失败')) } finally { busy.tls = false }
}
async function quickSelfSigned() {
  busy.tls = true
  try {
    const t: any = await apiPost('/api/admin/sb/tls/quick-selfsigned', { server_id: serverId.value || 0, name: '自签-' + (tag.value || scenario.value.tag) })
    await refreshTls()
    tlsId.value = t.id
    message.success('已生成自签证书')
  } catch (e: any) { message.error(errMsg(e, '生成失败')) } finally { busy.tls = false }
}
async function createReality() {
  busy.tls = true
  try {
    const sni = realitySni.value.trim()
    const t: any = await apiPost('/api/admin/sb/tls/reality', { name: 'reality-' + (tag.value || scenario.value.tag), server_id: serverId.value || 0, server_name: sni, handshake_server: sni })
    await refreshTls()
    tlsId.value = t.id
    message.success('Reality 配置已生成')
  } catch (e: any) { message.error(errMsg(e, '生成失败')) } finally { busy.tls = false }
}
// 进入证书步骤时若已有覆盖该域名的证书且未选，自动选中第一张
watch([cur, candidates], ([k, list]) => {
  if (k === 'cert' && !certId.value && list.length) certId.value = list[0].id
})
// 进入 TLS 步骤且证书已选好：自动创建 / 复用，省一次点击
watch(cur, k => { if (k === 'tls' && certId.value && !tlsId.value) ensureTls() })
// 换了证书时清掉不匹配的 tlsId，让 TLS 步骤重新匹配或创建
watch(() => certId.value, id => {
  if (!tlsId.value) return
  const t = tlsList.value.find(x => x.id === tlsId.value)
  if (!t || t.cert_id !== id) tlsId.value = 0
})

// 入站
async function saveInbound() {
  if (serverId.value == null) return
  busy.save = true
  try {
    const s = scenario.value
    const body = {
      type: s.type, tag: tag.value.trim(), listen: listen.value || '::', listen_port: port.value,
      tls_id: s.tls === 'none' ? 0 : (tlsId.value || 0), server_id: serverId.value, enabled: enabled.value,
      upstream_inbound_id: editingId.value ? existingUpstreamId.value : 0,
      egress_id: editingId.value ? existingEgressId.value : 0,
      options: inboundOptions(s, existingOptions.value),
    }
    const saved: any = editingId.value
      ? await apiPut('/api/admin/sb/inbounds/' + editingId.value, body)
      : await apiPost('/api/admin/sb/inbounds', body)
    savedId.value = saved?.id || editingId.value
    inbounds.value = await apiList('/api/admin/sb/inbounds')
    message.success('已保存，配置推送中…')
    cur.value = 'verify'
    reached.value = steps.value.length - 1
    await runVerify(false)
  } catch (e: any) { message.error(errMsg(e, '保存失败')) } finally { busy.save = false }
}

// 验证：配置是异步下发的，首次检测可能连不上，自动重试几次
const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))
async function runVerify(manual: boolean) {
  if (!savedId.value) return
  verify.value = null
  if (isQuic.value) { verifyHint.value = `${scenario.value.title} 基于 UDP/QUIC，面板无法用 TCP 握手检测。客户端通常不校验证书，连不上时请先检查 UDP 端口是否放行。`; return }
  if (scenario.value.tls !== 'cert' || !tlsId.value) { verifyHint.value = '该配置没有证书可检测。'; return }
  busy.verify = true
  try {
    const attempts = manual ? 1 : 4
    for (let i = 0; i < attempts; i++) {
      if (!manual || i > 0) await sleep(manual ? 0 : 3000)
      verify.value = await apiPost('/api/admin/sb/inbounds/' + savedId.value + '/tls-check', {})
      if (verify.value?.status !== 'error') break
    }
  } catch (e: any) { message.error(errMsg(e, '检测失败')) } finally { busy.verify = false }
}
function restart() {
  editingId.value = 0; showEditPicker.value = false; savedId.value = 0; verify.value = null; tlsId.value = 0; certId.value = 0
  existingOptions.value = ''; existingUpstreamId.value = 0; existingEgressId.value = 0; dnsRes.value = null; reached.value = 0
  applyScenarioDefaults(scenario.value); cur.value = 'goal'
  router.replace({ path: '/admin/setup' })
}
function onModeToggle(v: string) {
  if (v === 'create') { restart(); return }
  showEditPicker.value = true
  if (!editingId.value && editableInboundOpts.value.length) pickInboundToEdit(editableInboundOpts.value[0].value)
}
function pickInboundToEdit(id: number | null) {
  if (!id) return
  const ib = inbounds.value.find(n => n.id === id)
  if (!ib) return
  loadInboundIntoWizard(ib)
  router.replace({ path: '/admin/setup', query: { inbound: String(ib.id) } })
}
function loadInboundIntoWizard(ib: any): boolean {
  const tls = tlsList.value.find(t => t.id === ib.tls_id) || null
  const sc = scenarioForInbound(ib, tls)
  if (!sc) return false
  editingId.value = ib.id; showEditPicker.value = true; scenarioKey.value = sc.key
  serverId.value = ib.server_id || null
  tag.value = ib.tag; port.value = ib.listen_port; listen.value = ib.listen || '::'; enabled.value = ib.enabled
  existingOptions.value = typeof ib.options === 'string' ? ib.options : JSON.stringify(ib.options || {})
  existingUpstreamId.value = ib.upstream_inbound_id || 0
  existingEgressId.value = ib.egress_id || 0
  tlsId.value = ib.tls_id || 0
  certId.value = tls?.cert_id || 0
  useRealCert.value = canChooseCertMode(sc) ? !!tls?.cert_id : false
  onServerChange()
  cur.value = 'server'; reached.value = steps.value.length - 1
  return true
}

async function loadServers() { servers.value = await apiList('/api/admin/servers'); if (serverId.value != null) onServerChangeKeepDomain() }
function onServerChangeKeepDomain() { if (!domain.value && server.value?.domain) domain.value = server.value.domain }

async function bootstrap() {
  loading.value = true
  try {
    const [sv, ce, tl, ibs, zr, pf] = await Promise.all([
      apiList('/api/admin/servers'), apiList('/api/admin/certs'), apiList('/api/admin/sb/tls'), apiList('/api/admin/sb/inbounds'),
      apiGet<any>('/api/admin/cf/zones').catch(() => null), apiGet<any>('/api/admin/setup/preflight').catch(() => null),
    ])
    servers.value = sv; certs.value = ce; tlsList.value = tl; inbounds.value = ibs
    cf.configured = !!(zr?.configured ?? pf?.cf_configured); zones.value = zr?.zones || []
    pre.acme_installed = !!pf?.acme_installed; pre.acme_email = pf?.acme_email || ''; acmeEmail.value = pre.acme_email
    if (!cf.configured) acmeMethod.value = 'http-01'
    applyScenarioDefaults(scenario.value)
    await applyRoute()
  } catch (e: any) { message.error(errMsg(e, '加载失败')) } finally { loading.value = false }
}

// 入口：?inbound=ID 编辑入站；?server=ID 编辑服务器域名；?scenario=KEY 预选用途
async function applyRoute() {
  const q = route.query
  const want = typeof q.scenario === 'string' ? q.scenario : ''
  if (want && SCENARIOS.some(s => s.key === want)) { scenarioKey.value = want; applyScenarioDefaults(scenarioByKey(want)) }
  if (q.inbound) {
    const id = Number(q.inbound)
    const ib = inbounds.value.find((n: any) => n.id === id)
    if (!ib) { message.error('入站不存在'); return }
    if (!loadInboundIntoWizard(ib)) {
      message.warning('该协议暂不支持向导编辑，已为你打开 sing-box 页面')
      router.replace('/admin/singbox')
    }
    return
  }
  if (q.server) {
    const id = Number(q.server)
    if (servers.value.some(s => s.id === id)) {
      serverId.value = id; onServerChange()
      scenarioKey.value = 'https-proxy'; applyScenarioDefaults(scenarioByKey('https-proxy'))
      cur.value = 'domain'; reached.value = 2
    }
  }
}

onMounted(bootstrap)
</script>

<style scoped>
.setup-page { max-width: 880px; }
.setup-head { margin-bottom: 14px; }
.setup-card { border-radius: 14px; }
.mode-bar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 14px; }
.setup-steps { margin: 4px 0 18px; }
.setup-body { min-height: 240px; }
.sec-title { margin: 0 0 12px; font-size: 15px; font-weight: 650; }
.setup-note { margin: 8px 0; color: var(--text-3); font-size: 12.5px; line-height: 1.7; }
.setup-note code, .setup-page code { background: var(--border); padding: 0 4px; border-radius: 4px; }
.setup-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 10px; }
.setup-gap { margin-top: 12px; }
.setup-gap-b { margin-bottom: 12px; }
.setup-edit-tip { margin-bottom: 14px; }
.dot { color: var(--text-3); }
.goal-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 10px; }
.goal-card {
  display: flex; flex-direction: column; gap: 4px; text-align: left; padding: 12px 14px; cursor: pointer;
  border: 1px solid var(--border); border-radius: 12px; background: var(--card); color: var(--text); font: inherit;
}
.goal-card:hover:not(:disabled) { border-color: var(--accent-strong); }
.goal-card.active { border-color: var(--accent-strong); box-shadow: 0 0 0 1px var(--accent-strong) inset; }
.goal-card:disabled { opacity: .5; cursor: not-allowed; }
.goal-title { font-weight: 650; font-size: 14px; }
.goal-need { font-size: 11.5px; color: var(--accent-strong); }
.goal-desc { font-size: 12px; color: var(--text-3); line-height: 1.6; }
.radio-col { display: flex; flex-direction: column; gap: 8px; }
.acme-box { border: 1px solid var(--border); border-radius: 12px; padding: 12px 14px; background: var(--bg-soft); }
.form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
.form-grid label { display: flex; flex-direction: column; gap: 6px; font-size: 12.5px; color: var(--text-2); }
.verify-box { min-height: 60px; }
.verify-row { margin: 8px 0; font-size: 12.5px; }
.verify-row ul { margin: 4px 0 0; padding-left: 18px; display: grid; gap: 4px; line-height: 1.6; }
.v-problem { color: #b42318; }
.v-warn { color: #b54708; }
.v-note { color: var(--text-3); }
.setup-nav { display: flex; justify-content: space-between; margin-top: 20px; padding-top: 14px; border-top: 1px solid var(--border); }
</style>
