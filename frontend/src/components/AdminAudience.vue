<template>
  <section class="audience">
    <p>当前有权限使用：{{ total === null ? '—' : `${total} 位用户` }}，按用户去重，包含未使用流量的用户；不是在线人数。</p>
    <p class="scope">下方用量是所列套餐 / 额度的累计用量，不代表此节点或机器上的用量。免费节点可由免费权限开放。</p>
    <n-alert v-if="error" type="error">{{ error }} <n-button size="tiny" @click="load">重试</n-button></n-alert>
    <n-spin :show="loading">
      <div class="table-wrap"><table v-if="users.length"><thead><tr><th>用户</th><th>生效套餐 / 额度</th><th>累计已用</th><th>剩余 / 状态</th><th>额度 / 到期</th></tr></thead>
      <tbody><template v-for="user in users" :key="user.user_id">
        <tr v-for="(bucket, index) in user.buckets" :key="bucket.id"><td>{{ index === 0 ? `${user.username} (#${user.user_id})` : '' }}</td><td>{{ bucket.name || (bucket.kind === 'free' ? '免费流量' : '公共池 / 赠送额度') }}</td><td>{{ fmtBytes(bucket.used_up + bucket.used_down) }}</td><td>{{ bucket.kind === 'free' ? '不限量 · 免费计量' : `${fmtBytes(Math.max(0, bucket.traffic_limit - bucket.used_up - bucket.used_down))} · 生效中` }}</td><td>{{ bucket.kind === 'free' ? '免费计量' : fmtBytes(bucket.traffic_limit) }} · {{ bucket.expiry_at ? fmtDateTime(bucket.expiry_at) : '无到期时间' }}</td></tr>
        <tr v-if="!user.buckets.length"><td>{{ user.username }} (#{{ user.user_id }})</td><td>免费节点权限</td><td>—</td><td>免费开放</td><td>—</td></tr>
      </template></tbody></table><n-empty v-else-if="!loading && !error" description="暂无有权限的用户" /></div>
    </n-spin>
    <n-pagination v-if="total && total > 50" v-model:page="page" :item-count="total" :page-size="50" @update:page="load" />
  </section>
</template>
<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from 'vue'
import { NAlert, NButton, NEmpty, NPagination, NSpin } from 'naive-ui'
import { apiGet } from '@/api'
import { fmtBytes, fmtDateTime } from '@/utils/format'
const props = defineProps<{ scope: 'node' | 'group' | 'server' | 'package'; id: number }>()
const users = ref<any[]>([]), total = ref<number | null>(null), page = ref(1), loading = ref(false), error = ref('')
let request = 0
async function load() {
  const seq = ++request; loading.value = true; error.value = ''; users.value = []; total.value = null
  try {
    const data = await apiGet(`/api/admin/stats/audience?scope=${props.scope}&id=${props.id}&page=${page.value}`)
    if (seq !== request) return
    users.value = data.users; total.value = data.total
  } catch (e: any) { if (seq === request) error.value = e.message || '读取权限失败' }
  finally { if (seq === request) loading.value = false }
}
watch(() => [props.scope, props.id], () => { page.value = 1; void load() }, { immediate: true })
onBeforeUnmount(() => { request++ })
</script>
<style scoped>
.audience { font-size: 13px; } .scope { color: var(--text-3); } .table-wrap { overflow-x: auto; margin: 12px 0; } table { width:100%; border-collapse:collapse; } th,td { padding:10px; text-align:left; border-bottom:1px solid var(--border); white-space:nowrap; }
</style>
