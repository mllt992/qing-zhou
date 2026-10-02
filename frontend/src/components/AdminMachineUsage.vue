<template>
  <n-card title="机器 · 用户用量" size="small">
    <div class="controls"><n-select v-model:value="serverID" :options="servers" placeholder="选择机器" filterable style="min-width:180px; flex:1" /><n-select v-model:value="days" :options="[{label:'近 7 天',value:7},{label:'近 14 天',value:14},{label:'近 30 天',value:30}]" style="width:130px" /><n-button :disabled="serverID === null" @click="load">刷新</n-button></div>
    <p>统计此机器上实际记录的 sing-box 用户上传 + 下载，不是网卡 / 服务商计费流量。原始明细仅保留约 35 天，尚未记录、清理或采集失败的历史不能视为零，也无法追溯此机器的套餐归属。</p>
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-tabs v-if="serverID !== null" v-model:value="tab">
      <n-tab-pane name="traffic" tab="实际用量与排名">
        <n-spin :show="loading">
          <template v-if="report">
            <p>有记录用户 {{ report.user_count }} 位 · 上传 {{ fmtBytes(report.up) }} · 下载 {{ fmtBytes(report.down) }} · 合计 {{ fmtBytes(report.up + report.down) }}</p>
            <p v-if="report.coverage_start">本次查询的首 / 末条记录：{{ fmtDateTime(report.coverage_start) }} 至 {{ fmtDateTime(report.coverage_end) }}（不保证期间连续采集）</p>
            <n-empty v-else description="该时间范围暂无机器用量记录，不能据此判断没有使用" />
            <div v-if="report.users.length" class="table-wrap"><table><thead><tr><th>排名</th><th>用户</th><th>上传</th><th>下载</th><th>合计</th><th>占本机记录用量</th></tr></thead><tbody><tr v-for="(u, index) in report.users" :key="u.user_id"><td>{{ (page - 1) * 50 + index + 1 }}</td><td>{{ u.username }} (#{{ u.user_id }})</td><td>{{ fmtBytes(u.up) }}</td><td>{{ fmtBytes(u.down) }}</td><td><b>{{ fmtBytes(u.total) }}</b></td><td><progress :value="u.total" :max="Math.max(1, report.up + report.down)" :aria-label="`${u.username} 用量占比`" /> {{ (100 * u.total / Math.max(1, report.up + report.down)).toFixed(1) }}%</td></tr></tbody></table></div>
            <n-pagination v-if="report.user_count > 50" v-model:page="page" :item-count="report.user_count" :page-size="50" @update:page="load" />
            <details v-if="report.days.length"><summary>每日用量趋势（只展示有记录的日期）</summary><div v-for="day in report.days" :key="day.date" class="day"><span>{{ day.date }}</span><progress :value="day.up + day.down" :max="maxDay" :aria-label="`${day.date} 用量`" /><b>{{ fmtBytes(day.up + day.down) }}</b></div></details>
          </template>
        </n-spin>
      </n-tab-pane>
      <n-tab-pane name="eligible" tab="当前可用用户（含零用量）"><AdminAudience v-if="tab === 'eligible'" scope="server" :id="serverID" /></n-tab-pane>
    </n-tabs>
  </n-card>
</template>
<script setup lang="ts">
import { computed, ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { NAlert, NButton, NCard, NEmpty, NPagination, NSelect, NSpin, NTabs, NTabPane } from 'naive-ui'
import { apiGet, apiList } from '@/api'
import { fmtBytes, fmtDateTime } from '@/utils/format'
import AdminAudience from './AdminAudience.vue'
const servers = ref<{label:string;value:number}[]>([]), serverID = ref<number | null>(null), days = ref(30), page = ref(1), tab = ref('traffic'), loading = ref(false), error = ref(''), report = ref<any>(null)
const maxDay = computed(() => Math.max(1, ...(report.value?.days || []).map((d:any) => d.up + d.down)))
let request = 0, alive = true
async function load() {
  const seq = ++request; report.value = null; error.value = ''
  if (serverID.value === null) return
  loading.value = true
  try { const d = await apiGet(`/api/admin/stats/usage/machine?server=${serverID.value}&days=${days.value}&page=${page.value}`); if (seq === request) report.value = d.report }
  catch (e:any) { if (seq === request) error.value = e.message || '读取用量失败' }
  finally { if (seq === request) loading.value = false }
}
watch([serverID, days], () => { page.value = 1; void load() })
onMounted(async () => { try { const list = await apiList('/api/admin/servers'); if (alive) servers.value = [{ label: '面板本机', value: 0 }, ...list.filter(s => s.id !== 0).map(s => ({ label: s.name || `机器 #${s.id}`, value: s.id }))] } catch (e:any) { if (alive) error.value = e.message } })
onBeforeUnmount(() => { alive = false; request++ })
</script>
<style scoped>
.controls {display:flex;gap:10px;flex-wrap:wrap} p {font-size:12px;color:var(--text-3)} .table-wrap {overflow:auto;margin:12px 0} table {width:100%;border-collapse:collapse} th,td {padding:10px;text-align:left;white-space:nowrap;border-bottom:1px solid var(--border)} progress {accent-color:var(--primary);max-width:150px} details {margin-top:16px} .day {display:flex;gap:12px;align-items:center;margin:8px 0;flex-wrap:wrap}
</style>
