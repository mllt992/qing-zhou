<template>
  <div>
    <div class="page-head"><div><h2 class="page-title">账户设置</h2><p class="page-sub">身份信息、通知渠道、密码与登录设备集中管理</p></div></div>
    <div class="resource-overview account-overview">
      <div class="resource-metric"><b>{{ auth.user?.points || 0 }}</b><span>可用积分 · {{ yuan(auth.user?.points || 0) }}</span></div>
      <div class="resource-metric" :class="auth.user?.email_verified ? 'success' : 'warn'"><b>{{ auth.user?.email_verified ? '已验证' : auth.user?.email ? '待验证' : '未绑定' }}</b><span>账户邮箱</span></div>
      <div class="resource-metric" :class="tg.bound ? 'success' : ''"><b>{{ tg.bound ? '已绑定' : tg.enabled ? '可绑定' : '未启用' }}</b><span>Telegram 通知</span></div>
      <div class="resource-metric"><b>{{ sessions.length }}</b><span>登录会话 · 可逐个退出</span></div>
    </div>

    <!-- 基本信息 -->
    <n-card title="基本信息" size="small" style="margin-bottom:16px;">
      <n-descriptions :column="2" bordered size="small">
        <n-descriptions-item label="用户名">{{ auth.user?.username }}</n-descriptions-item>
        <n-descriptions-item label="邮箱">
          {{ auth.user?.email || '未绑定' }}
          <n-tag v-if="auth.user?.email && auth.user?.email_verified" type="success" size="tiny" bordered style="margin-left:6px;">已验证</n-tag>
          <n-tag v-else-if="auth.user?.email" type="warning" size="tiny" bordered style="margin-left:6px;">未验证</n-tag>
        </n-descriptions-item>
        <n-descriptions-item label="积分">{{ auth.user?.points || 0 }} ({{ yuan(auth.user?.points || 0) }})</n-descriptions-item>
        <n-descriptions-item label="角色">{{ auth.user?.is_admin ? '管理员' : '用户' }}</n-descriptions-item>
      </n-descriptions>
    </n-card>

    <OAuth2Account />
    <!-- 邮箱设置 -->
    <n-card title="邮箱设置" size="small" style="margin-bottom:16px;">
      <n-form label-placement="left" label-width="80" style="max-width:400px;">
        <n-form-item label="邮箱">
          <n-input v-model:value="emailForm.email" placeholder="输入邮箱地址" />
        </n-form-item>
        <n-form-item>
          <n-space>
            <n-button type="primary" :loading="bindingEmail" @click="handleBindEmail">绑定邮箱</n-button>
            <n-button v-if="auth.user?.email && !auth.user?.email_verified" :loading="resending" @click="handleResendVerify">发送验证邮件</n-button>
          </n-space>
        </n-form-item>
      </n-form>
      <p v-if="auth.user?.email && !auth.user?.email_verified" style="font-size:12px;color:var(--text-3);margin:0;">
        验证邮件可能需要几分钟，请检查垃圾邮件文件夹
      </p>
    </n-card>

    <!-- Telegram -->
    <n-card title="Telegram 绑定" size="small" style="margin-bottom:16px;">
      <template v-if="!tg.enabled">
        <p class="hint">管理员尚未配置 Telegram Bot，绑定后可在聊天里查询订阅、套餐和流量，并接收到期 / 流量不足提醒。</p>
      </template>
      <template v-else-if="tg.bound">
        <n-descriptions :column="2" bordered size="small" style="margin-bottom:12px;">
          <n-descriptions-item label="Telegram">
            {{ tg.username ? '@' + tg.username : ('ID ' + tg.telegram_id) }}
            <n-tag type="success" size="tiny" bordered style="margin-left:6px;">已绑定</n-tag>
          </n-descriptions-item>
          <n-descriptions-item label="绑定时间">{{ fmtDateTime(tg.bound_at) }}</n-descriptions-item>
        </n-descriptions>
        <n-space align="center" style="margin-bottom:12px;">
          <span class="hint" style="margin:0;">到期提醒</span>
          <n-switch size="small" :value="tg.notify_expiry" @update:value="v => saveNotify({ notify_expiry: v })" />
          <span class="hint" style="margin:0 0 0 12px;">流量不足提醒</span>
          <n-switch size="small" :value="tg.notify_traffic" @update:value="v => saveNotify({ notify_traffic: v })" />
        </n-space>
        <n-button size="small" @click="handleUnbindTg">解除绑定</n-button>
      </template>
      <template v-else>
        <p class="hint">绑定后可在 Telegram 里发送 /sub、/plan、/traffic 查询，并接收到期和流量不足通知。订阅地址请当作密码保管。</p>
        <n-space>
          <n-button type="primary" :loading="bindingTg" @click="handleBindTg">生成绑定链接</n-button>
          <n-button v-if="tgLink" tag="a" :href="tgLink" target="_blank" rel="noopener">打开 Telegram</n-button>
          <n-button v-if="tgLink" quaternary @click="copyTgLink">复制链接</n-button>
        </n-space>
        <p v-if="tgLink" class="hint" style="margin-top:8px;">链接 15 分钟内有效，打开后点「开始」即可完成绑定。</p>
      </template>
    </n-card>

    <!-- 修改密码 -->
    <n-card title="修改密码" size="small" style="margin-bottom:16px;">
      <n-form label-placement="left" label-width="80" style="max-width:400px;">
        <n-form-item label="旧密码"><n-input v-model:value="pwForm.old" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="新密码"><n-input v-model:value="pwForm.new" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="确认密码"><n-input v-model:value="pwForm.confirm" type="password" show-password-on="click" /></n-form-item>
        <n-form-item>
          <n-button type="primary" :loading="changingPw" @click="handleChangePw">修改密码</n-button>
        </n-form-item>
      </n-form>
      <p style="font-size:12px;color:var(--text-3);margin:0;">修改密码后，其他设备将被要求重新登录</p>
    </n-card>

    <!-- 登录会话 -->
    <n-card title="登录会话" size="small">
      <n-data-table :columns="sessionCols" :data="sortedSessions" :bordered="false" size="small" :loading="loadingSessions" :pagination="{pageSize:5}" />
    </n-card>
  </div>
</template>

<script setup lang="ts">
import OAuth2Account from '@/components/OAuth2Account.vue'
import { ref, reactive, computed, onMounted, onUnmounted, h } from 'vue'
import {
  NCard, NForm, NFormItem, NInput, NButton, NDescriptions, NDescriptionsItem,
  NDataTable, NTag, NSpace, NSwitch, useMessage
} from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import { apiGet, apiList, apiPost, apiPut } from '@/api'
import { fmtDateTime, yuan } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

const auth = useAuthStore()
const message = useMessage()

const pwForm = reactive({ old: '', new: '', confirm: '' })
const emailForm = reactive({ email: '' })
const changingPw = ref(false)
const bindingEmail = ref(false)
const resending = ref(false)
const sessions = ref<any[]>([])
const loadingSessions = ref(false)
const tg = reactive({
  enabled: false,
  bound: false,
  username: '',
  telegram_id: 0,
  notify_expiry: true,
  notify_traffic: true,
  bound_at: 0,
})
const tgLink = ref('')
const bindingTg = ref(false)
let tgPoll: ReturnType<typeof setInterval> | null = null

function stopTgPoll() {
  if (tgPoll) { clearInterval(tgPoll); tgPoll = null }
}

async function loadTelegram() {
  try {
    const data = await apiGet<any>('/api/user/telegram')
    if (data) Object.assign(tg, data)
    if (tg.bound) { tgLink.value = ''; stopTgPoll() }
  } catch {}
}

async function handleBindTg() {
  bindingTg.value = true
  try {
    const data = await apiPost<{ url: string }>('/api/user/telegram/bind-token')
    tgLink.value = data?.url || ''
    if (tgLink.value) {
      message.success('绑定链接已生成')
      stopTgPoll()
      tgPoll = setInterval(loadTelegram, 2500)
    }
  } catch (e: any) { message.error(e.message) } finally { bindingTg.value = false }
}

async function copyTgLink() {
  if (!tgLink.value) return
  if (await copyText(tgLink.value)) message.success('已复制')
  else message.error('复制失败，请手动复制')
}

async function handleUnbindTg() {
  try {
    await apiPost('/api/user/telegram/unbind')
    tg.bound = false
    tg.username = ''
    tgLink.value = ''
    message.success('已解绑')
  } catch (e: any) { message.error(e.message) }
}

async function saveNotify(patch: { notify_expiry?: boolean; notify_traffic?: boolean }) {
  try {
    const data = await apiPut<any>('/api/user/telegram/notify', patch)
    if (data) {
      if (typeof data.notify_expiry === 'boolean') tg.notify_expiry = data.notify_expiry
      if (typeof data.notify_traffic === 'boolean') tg.notify_traffic = data.notify_traffic
    }
  } catch (e: any) { message.error(e.message); await loadTelegram() }
}

/** 解析 UA 为可读描述 */
function parseUA(ua: string): string {
  if (!ua) return '未知设备'
  const os = ua.includes('Windows') ? 'Windows' : ua.includes('Mac') ? 'macOS' : ua.includes('Linux') ? 'Linux' : ua.includes('Android') ? 'Android' : ua.includes('iPhone') || ua.includes('iOS') ? 'iOS' : '未知'
  const br = ua.includes('Edg/') ? 'Edge' : ua.includes('Chrome') ? 'Chrome' : ua.includes('Firefox') ? 'Firefox' : ua.includes('Safari') ? 'Safari' : ua.includes('curl') ? 'curl' : '浏览器'
  return `${os} · ${br}`
}

/** 判断是否当前设备（粗略匹配） */
function isCurrentDevice(s: any): boolean {
  // 当前设备是最新登录的那个
  return sessions.value.length > 0 && s.id === sessions.value[0]?.id
}

const sortedSessions = computed(() => {
  return [...sessions.value].sort((a, b) => {
    if (isCurrentDevice(a)) return -1
    if (isCurrentDevice(b)) return 1
    return (b.created_at || 0) - (a.created_at || 0)
  })
})

const sessionCols = [
  {
    title: '设备', key: 'user_agent',
    render: (r: any) => h('div', [
      h('span', { style: 'font-weight:600;' }, parseUA(r.user_agent)),
      isCurrentDevice(r) ? h(NTag, { type: 'success', size: 'tiny', bordered: false, style: 'margin-left:6px;' }, { default: () => '当前设备' }) : null,
    ]),
  },
  { title: 'IP', key: 'ip', width: 130 },
  { title: '登录时间', key: 'created_at', width: 150, render: (r: any) => fmtDateTime(r.created_at) },
  {
    title: '操作', key: 'act', width: 70,
    render: (r: any) => isCurrentDevice(r) ? null : h(NButton, { size: 'tiny', type: 'error', onClick: () => revokeSession(r.id) }, { default: () => '撤销' }),
  },
]

async function handleChangePw() {
  if (pwForm.new !== pwForm.confirm) { message.error('两次密码不一致'); return }
  if (!pwForm.new || pwForm.new.length < 6) { message.error('密码至少6位'); return }
  changingPw.value = true
  try {
    await apiPost('/api/user/password', { old_password: pwForm.old, new_password: pwForm.new })
    message.success('密码已修改，其他设备需重新登录')
    pwForm.old = ''; pwForm.new = ''; pwForm.confirm = ''
  } catch (e: any) { message.error(e.message) } finally { changingPw.value = false }
}

async function handleBindEmail() {
  if (!emailForm.email) { message.warning('请输入邮箱'); return }
  bindingEmail.value = true
  try { await apiPost('/api/user/email', { email: emailForm.email }); message.success('绑定成功'); await auth.fetchMe() } catch (e: any) { message.error(e.message) } finally { bindingEmail.value = false }
}

async function handleResendVerify() {
  resending.value = true
  try { await apiPost('/api/user/resend-verify'); message.success('验证邮件已发送') } catch (e: any) { message.error(e.message) } finally { resending.value = false }
}

async function revokeSession(id: number) {
  try { await apiPost(`/api/user/sessions/${id}/revoke`); sessions.value = sessions.value.filter(s => s.id !== id); message.success('已撤销') } catch (e: any) { message.error(e.message) }
}

onMounted(async () => {
  emailForm.email = auth.user?.email || ''
  loadingSessions.value = true
  try { sessions.value = await apiList('/api/user/sessions') } catch {} finally { loadingSessions.value = false }
  await loadTelegram()
})
onUnmounted(stopTgPoll)
</script>

<style scoped>
.account-overview .resource-metric b { font-size:17px; }
</style>

<style scoped>
.page-title { font-size: 21px; margin-bottom: 4px; }
.page-sub { color: var(--text-2); margin-bottom: 22px; }
.hint { font-size: 12px; color: var(--text-3); margin: 0 0 12px; line-height: 1.7; }
</style>
