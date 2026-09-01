<!-- 管理端「一份额度」的行。套餐面板的聚合视图和平铺视图共用同一个渲染，
     否则展开后的明细和平铺列表迟早会在口径或措辞上分叉。 -->
<template>
  <div class="pi" :class="{ queued: bucket === 'queued', done: bucket === 'finished' }">
    <div class="pi-top">
      <span class="pi-name" :title="plan.name">{{ plan.name || '份 #' + plan.id }}</span>
      <n-tag :type="meta.type" size="tiny" :bordered="false">{{ meta.label }}</n-tag>
      <span class="pi-num">{{ amountText }}</span>
    </div>
    <!-- 排队中的份还没开始计量，画一条斜纹而不是 0% 的进度条：后者看起来像
         「有额度但一点没用」，实际上它现在根本不发节点。 -->
    <div v-if="bucket === 'queued'" class="pi-queued-bar" />
    <div v-else class="bar">
      <div class="bar-fill" :style="{ width: fillWidth, background: fillColor }" />
    </div>
    <div class="pi-foot">
      <span class="pi-when">{{ whenText }}</span>
      <span class="spacer" />
      <n-button v-if="canAdjust" size="tiny" quaternary :disabled="removing" @click="$emit('adjust')">调整</n-button>
      <n-button size="tiny" type="error" quaternary :loading="removing" @click="$emit('remove')">
        {{ plan.kind === 'pool' ? '清空' : '移除' }}
      </n-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { NTag, NButton } from 'naive-ui'
import { fmtBytes, fmtDate, fmtDateTime, pct } from '@/utils/format'
import { planStatusMeta, planTimeText } from '@/utils/plan'

const props = defineProps<{ plan: any; removing?: boolean }>()
defineEmits<{ (e: 'remove'): void; (e: 'adjust'): void }>()

const meta = computed(() => planStatusMeta(props.plan))
const bucket = computed<'active' | 'queued' | 'finished'>(() =>
  meta.value.label === '使用中' ? 'active' : meta.value.label === '排队中' ? 'queued' : 'finished')
// 已过期的份加流量不会重新生效；零额度/已用尽仍可加，好把这一份救回来。
const canAdjust = computed(() => {
  return meta.value.label !== '已过期'
})

const usedPct = computed(() => pct(props.plan.used, props.plan.traffic_limit))
const fillWidth = computed(() =>
  props.plan.traffic_limit > 0 ? Math.min(usedPct.value, 100) + '%' : '0%')
const fillColor = computed(() => {
  if (bucket.value === 'finished') return 'var(--text-3)'
  if (props.plan.traffic_limit <= 0) return 'var(--text-3)'
  return usedPct.value > 90 ? '#c2685c' : usedPct.value > 70 ? '#bf9540' : '#6f8f76'
})

const amountText = computed(() => {
  const p = props.plan
  return `${fmtBytes(p.used)} / ${fmtBytes(p.traffic_limit)}`
})

const whenText = computed(() => {
  const p = props.plan
  const when = planTimeText(p, fmtDate)
  const born = p.created_at ? `开通 ${fmtDateTime(p.created_at)}` : ''
  const from = p.order_id ? `订单 #${p.order_id}` : sourceLabel(p)
  return [when, born, from].filter(Boolean).join(' · ')
})
// 没有订单号的份不是「来路不明」，而是这三种赠予之一，说清楚比留白有用
function sourceLabel(p: any) {
  if (p.kind === 'pool') return '流量包余额'
  if (p.package_id === 0) return '管理员额度'
  if (p.package_id === -1) return '注册赠送'
  return '管理员分配'
}
</script>

<style scoped>
.pi { background: var(--bg-soft); border-radius: 8px; padding: 8px 10px; }
.pi.queued { background: transparent; border: 1px dashed var(--border); }
.pi.done { opacity: .68; }
.pi-top { display: flex; align-items: center; gap: 8px; min-width: 0; }
.pi-name { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.pi-num { margin-left: auto; font-size: 12px; font-variant-numeric: tabular-nums; color: var(--text-2); white-space: nowrap; }
.bar { height: 5px; border-radius: 3px; background: var(--border); overflow: hidden; margin-top: 6px; }
.bar-fill { height: 100%; border-radius: 3px; transition: width .6s cubic-bezier(.22, 1, .36, 1), background .3s ease; }
.pi-queued-bar {
  height: 5px; border-radius: 3px; margin-top: 6px;
  background: repeating-linear-gradient(45deg, var(--border), var(--border) 4px, transparent 4px, transparent 8px);
}
.pi-foot { display: flex; align-items: center; gap: 8px; margin-top: 5px; }
.pi-foot .spacer { flex: 1; }
.pi-when { font-size: 11px; color: var(--text-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (prefers-reduced-motion: reduce) { .bar-fill { transition: none; } }
</style>
