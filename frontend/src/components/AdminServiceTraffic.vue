<template>
  <section class="service-traffic" aria-label="本机代理业务来源">
    <div class="section-head"><h3>本机代理业务来源</h3><span v-if="service.new_coverage_start">扩展来源记录自 {{ fmtDateTime(service.new_coverage_start) }}</span></div>
    <p class="explanation">业务上下行来自本机 sing-box，与网卡 IN＋OUT 口径不同。中转在每台机器分别观测，用户套餐只在入口扣一次</p>
    <div class="service-totals">
      <span>代理业务 <b>{{ fmtBytes(service.total) }}</b></span>
      <span>本机入口付费计量 <b>{{ fmtBytes(service.billable_total) }}</b></span>
      <span>采集模式 <b>{{ service.quality.mode === 'cumulative' ? '累计快照' : '读出清零' }}</b></span>
    </div>
    <p v-if="service.quality.status !== 'ok'" class="warning" role="status">{{ qualityLabel }}。缺少统计不代表零流量</p>
    <p v-if="service.quality.pending_polls" class="warning" role="status">{{ service.quality.pending_polls }} 个采集批次等待入库重试，已成功的记录不会重复扣费</p>
    <p v-if="service.quality.gaps" class="warning">本周期有 {{ service.quality.gaps }} 个统计边界或缺口，不能视为完整覆盖</p>
    <p v-if="!service.user_coverage_complete" class="explanation">用户来源尚不完整，暂停按人数估算容量；中转汇总不会平均分摊给用户</p>
    <ul v-if="service.sources.length" class="sources">
      <li v-for="row in visibleSources" :key="`${row.kind}:${row.link_id}:${row.user_id}`">
        <div><span class="kind">{{ kindLabel(row.kind) }}</span><b>{{ row.name }}</b><strong>{{ fmtBytes(row.total) }}</strong></div>
        <small>上行 {{ fmtBytes(row.up) }} · 下行 {{ fmtBytes(row.down) }}<template v-if="isRelay(row.kind)"> · 仅观测，不重复扣费</template></small>
      </li>
    </ul>
    <p v-else class="empty">尚无本机代理业务记录，不能据此判断没有使用</p>
    <div v-if="service.sources.length > pageSize" class="pagination">
      <button :disabled="page === 1" @click="page--">上一页</button><span>{{ page }} / {{ pages }}</span><button :disabled="page >= pages" @click="page++">下一页</button>
    </div>
    <details v-if="service.outbound_links?.length"><summary>发送侧链路观测（不重复计入上方合计）</summary>
      <ul class="sources"><li v-for="row in service.outbound_links" :key="row.link_id"><div><b>{{ row.name }}</b><strong>{{ fmtBytes(row.total) }}</strong></div><small>上行 {{ fmtBytes(row.up) }} · 下行 {{ fmtBytes(row.down) }} · 独立出口观测，不是新增用户消费</small></li></ul>
    </details>
    <p class="history">旧共享中转无法追溯到具体入口或用户；升级前已被丢弃的记录不能补回。旧用户记录仅保留当时的已识别部分</p>
  </section>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { fmtBytes, fmtDateTime } from '@/utils/format'
interface Source { kind:string; link_id:number; user_id:number; name:string; up:number; down:number; total:number }
interface Service { total:number; billable_total:number; new_coverage_start:number; user_coverage_complete:boolean; sources:Source[]; outbound_links?:Source[]; quality:{mode:string; status:string; gaps:number; pending_polls:number} }
const props = defineProps<{service:Service}>()
const page = ref(1), pageSize = 10
const pages = computed(() => Math.max(1, Math.ceil(props.service.sources.length / pageSize)))
const visibleSources = computed(() => props.service.sources.slice((page.value-1)*pageSize,page.value*pageSize))
watch(() => props.service, () => { page.value=1 })
const qualityLabel = computed(() => (({unknown:'统计状态尚未确认', unavailable:'用户统计采集失败', unsupported:'统计能力未就绪', disabled:'统计未启用', stale:'用户统计已延迟', legacy:'进程代次无法验证，保留旧式采集'} as Record<string,string>)[props.service.quality.status] || '统计需要核查'))
function kindLabel(kind:string) { return ({direct_user:'直连用户',historical_user:'旧用户记录',relay_link:'中转链路',legacy_shared_relay:'旧共享中转',ambiguous_identity:'身份冲突',unknown:'未知身份'} as Record<string,string>)[kind] || '未知身份' }
function isRelay(kind:string) { return ['relay_link','legacy_shared_relay','ambiguous_identity','unknown'].includes(kind) }
</script>
<style scoped>
.service-traffic{padding:24px 0;border-top:1px solid var(--border)}.section-head{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px}.section-head h3{margin:0;font-size:16px}.section-head span,.explanation,.history,.empty{color:var(--text-3);font-size:12px;line-height:1.8}.service-totals{display:flex;flex-wrap:wrap;gap:20px;background:var(--bg-soft);padding:14px;border-radius:10px;font-size:12px}.warning{font-size:12px;color:var(--warning,#a86620);line-height:1.7}.sources{padding:0;list-style:none}.sources li{padding:12px 0;border-bottom:1px solid var(--border)}.sources li>div{display:flex;align-items:center;gap:8px}.sources strong{margin-left:auto;font-size:13px}.sources b{font-size:13px;overflow-wrap:anywhere}.sources small{display:block;color:var(--text-3);margin-top:6px}.kind{font-size:10px;padding:3px 6px;border-radius:4px;background:var(--bg-soft);white-space:nowrap}.pagination{display:flex;justify-content:center;gap:14px;align-items:center;font-size:12px}.pagination button{background:var(--card);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:5px 10px}.pagination button:disabled{opacity:.5}.history{margin-top:18px}
</style>
