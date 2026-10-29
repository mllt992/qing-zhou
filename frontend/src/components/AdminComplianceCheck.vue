<!-- 合规检测：按服务商常见规则检查每台机器（公网 HTTP/SOCKS 代理、Web 端口上的网站）。
     只读，不会修改任何配置。 -->
<template>
  <n-button :loading="running" @click="start">合规检测</n-button>
  <n-modal v-model:show="show" preset="card" title="合规检测" style="max-width:760px;">
    <p class="cc-sub">
      按服务商的常见规定检查每台机器：公网上不设限制的 HTTP/SOCKS 代理，以及 80/443/8080/8443 端口上不设限制的网站（包括面板和伪装站点）。
      检测只读取配置和端口情况，不会修改任何东西。
    </p>
    <n-spin :show="running">
      <div v-if="result" class="cc-sum">
        <span class="cc-pill high">高危 {{ result.counts?.high || 0 }}</span>
        <span class="cc-pill medium">中危 {{ result.counts?.medium || 0 }}</span>
        <span class="cc-pill low">低危 {{ result.counts?.low || 0 }}</span>
        <span class="cc-pill info">提示 {{ result.counts?.info || 0 }}</span>
        <span class="cc-time">检测于 {{ fmtDateTime(result.checked_at) }}</span>
        <n-button size="tiny" quaternary :disabled="running" @click="start">重新检测</n-button>
      </div>
      <div v-if="result" class="cc-list">
        <div v-for="s in result.servers" :key="s.server_id" class="cc-srv">
          <div class="cc-srv-h">
            <b>{{ s.name }}</b><span class="cc-host">{{ s.host }}</span>
            <n-tag size="tiny" :bordered="false" :type="worst(s) === 'high' ? 'error' : worst(s) === 'medium' ? 'warning' : 'success'">
              {{ s.findings.length ? `${s.findings.length} 项需要关注` : '未发现问题' }}
            </n-tag>
          </div>
          <div v-if="s.config_note || s.live_note" class="cc-note">
            {{ [s.config_note, s.live_note].filter(Boolean).join('；') }}
          </div>
          <div v-for="(f, i) in s.findings" :key="i" class="cc-f" :class="f.severity">
            <div class="cc-f-t"><span class="cc-sev">{{ sevLabel[f.severity] || f.severity }}</span>{{ f.title }}</div>
            <div class="cc-f-d">{{ f.detail }}</div>
            <div class="cc-f-fix">建议：{{ f.fix }}</div>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!running" description="点击「合规检测」开始" size="small" style="padding:24px 0;" />
    </n-spin>
    <p class="cc-foot">
      检测基于规则，无法看到云服务商安全组里的限制；已经在安全组里限制了来源 IP 的项目可以忽略。
    </p>
  </n-modal>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NModal, NSpin, NTag, NEmpty, useMessage } from 'naive-ui'
import { apiPost } from '@/api'
import { fmtDateTime } from '@/utils/format'

const message = useMessage()
const show = ref(false)
const running = ref(false)
const result = ref<any>(null)
const sevLabel: Record<string, string> = { high: '高危', medium: '中危', low: '低危', info: '提示' }
const order = ['high', 'medium', 'low', 'info']

function worst(s: any): string {
  for (const k of order) if (s.findings?.some((f: any) => f.severity === k)) return k
  return ''
}

async function start() {
  if (running.value) return
  show.value = true
  running.value = true
  try {
    result.value = await apiPost('/api/admin/compliance/check')
    const c = result.value?.counts || {}
    if (c.high) message.warning(`发现 ${c.high} 项高危问题，请尽快处理`)
    else message.success('检测完成')
  } catch (e: any) {
    message.error('检测失败：' + (e?.message || '请稍后重试'))
  } finally { running.value = false }
}
</script>

<style scoped>
.cc-sub, .cc-foot { font-size: 12px; color: var(--text-3); line-height: 1.6; margin: 0 0 10px; }
.cc-foot { margin: 12px 0 0; }
.cc-sum { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.cc-pill { font-size: 12px; padding: 2px 8px; border-radius: 10px; background: var(--bg-soft); }
.cc-pill.high { color: #b6413a; } .cc-pill.medium { color: #a77a22; } .cc-pill.low { color: #5e7a99; }
.cc-time { margin-left: auto; font-size: 11px; color: var(--text-3); }
.cc-list { display: flex; flex-direction: column; gap: 10px; max-height: 60vh; overflow: auto; }
.cc-srv { border: 1px solid var(--border); border-radius: 8px; padding: 10px; }
.cc-srv-h { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.cc-host { font-size: 12px; color: var(--text-3); margin-right: auto; }
.cc-note { font-size: 12px; color: var(--text-3); margin-bottom: 6px; }
.cc-f { border-left: 3px solid var(--border); padding: 6px 10px; margin-top: 6px; background: var(--bg-soft); border-radius: 0 6px 6px 0; }
.cc-f.high { border-left-color: #b6413a; } .cc-f.medium { border-left-color: #bf9540; } .cc-f.low { border-left-color: #5e7a99; }
.cc-f-t { font-size: 13px; font-weight: 600; }
.cc-sev { font-size: 11px; font-weight: 500; margin-right: 6px; color: var(--text-2); }
.cc-f-d, .cc-f-fix { font-size: 12px; color: var(--text-2); line-height: 1.6; margin-top: 2px; }
.cc-f-fix { color: var(--text-1, inherit); }
</style>
