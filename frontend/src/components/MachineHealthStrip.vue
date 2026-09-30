<template>
  <n-card size="small" class="sec health-card">
    <template #header>
      <span class="sec-title">机器健康</span>
      <span class="sec-note">各机器同一条时间轴 · 颜色是探针有没有在报，深浅才是流量</span>
    </template>
    <template #header-extra>
      <n-radio-group v-model:value="preset" size="small" @update:value="load">
        <n-radio-button v-for="r in presets" :key="r.v" :value="r.v">{{ r.l }}</n-radio-button>
      </n-radio-group>
    </template>

    <p class="health-note">
      读的是已经存下的探针记录，大约每分钟一条，保留 35 天。探针不报延迟和丢包，所以这里看不出「变慢」，只能看出中断。
      没人用是正常的浅色，不是故障；斜线才是探针没上报，不会画成零流量。
    </p>

    <n-spin :show="loading">
      <n-empty v-if="ready && !machines.length" description="还没有采样" style="padding:28px 0;" />
      <template v-else-if="ready">
        <div v-if="plotted.length" class="legend">
          <span><i class="sw idle" />在报、几乎没流量</span>
          <span><i class="sw busy" />在报、流量大（越深越大）</span>
          <span><i class="sw bridge" />这一档没有采样（还没到中断）</span>
          <span><i class="sw gap" />探针中断</span>
        </div>
        <div v-if="plotted.length" class="heat">
          <div class="labels">
            <div class="axis-spacer" />
            <button
              v-for="m in plotted"
              :key="m.server_id"
              type="button"
              class="lab"
              :title="'查看' + m.name + '的流量状态'"
              @click="open(m)"
            >
              <span class="lab-name">{{ m.name }}</span>
              <span v-if="m.local" class="lab-tag">本机</span>
            </button>
          </div>
          <div class="plot">
            <canvas
              ref="canvasEl"
              class="heat-canvas"
              @mousemove="onMove"
              @mouseleave="tip = null"
              @click="onClick"
            />
            <div v-if="tip" class="tip" :style="{ left: tip.x + 'px', top: tip.y + 'px' }">
              <b>{{ tip.name }}</b>
              <span>{{ tip.when }}</span>
              <span>{{ tip.detail }}</span>
            </div>
          </div>
        </div>
        <ul v-if="empties.length" class="empty-list">
          <li v-for="m in empties" :key="m.server_id">
            <button type="button" class="lab linkish" @click="open(m)">{{ m.name }}</button>
            <span>还没有采样</span>
          </li>
        </ul>
      </template>
    </n-spin>
  </n-card>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NCard, NEmpty, NRadioButton, NRadioGroup, NSpin, useMessage } from 'naive-ui'
import { apiGet } from '@/api'
import { fmtBytes, fmtDateTime } from '@/utils/format'

type Cell = { ts: number, state: 'online' | 'gap' | 'bridge', bytes?: number, known?: boolean }
type Strip = {
  server_id: number
  name: string
  local: boolean
  empty: boolean
  sample_count: number
  max_bytes: number
  cells: Cell[]
}
type Timeline = {
  from: number
  to: number
  bucket_sec: number
  gap_after_sec: number
  machines: Strip[]
}

const router = useRouter()
const message = useMessage()
const presets = [
  { v: '6h', l: '近 6 小时', sec: 6 * 3600 },
  { v: '24h', l: '近 24 小时', sec: 24 * 3600 },
  { v: '7d', l: '近 7 天', sec: 7 * 86400 },
  { v: '30d', l: '近 30 天', sec: 30 * 86400 },
]
const preset = ref('24h')
const loading = ref(false)
const ready = ref(false)
const tl = ref<Timeline | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)
const tip = ref<{ x: number, y: number, name: string, when: string, detail: string } | null>(null)

const machines = computed(() => tl.value?.machines || [])
const plotted = computed(() => machines.value.filter(m => m.cells?.length))
const empties = computed(() => machines.value.filter(m => !m.cells?.length))

const AXIS = 22
const ROW = 26
const IDLE = '#e4eee8'
const BUSY = '#3f6b52'
const BRIDGE = '#efe6d4'
const UNKNOWN = '#d5ddd8'

function mix(a: string, b: string, t: number) {
  const p = (hex: string) => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16))
  const [ar, ag, ab] = p(a)
  const [br, bg, bb] = p(b)
  const u = Math.max(0, Math.min(1, t))
  const c = (x: number, y: number) => Math.round(x + (y - x) * u)
  return `rgb(${c(ar, br)}, ${c(ag, bg)}, ${c(ab, bb)})`
}

function tickLabel(ts: number) {
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  const span = (tl.value?.to || 0) - (tl.value?.from || 0)
  if (span <= 48 * 3600) return `${p(d.getHours())}:${p(d.getMinutes())}`
  return `${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:00`
}

function paintGap(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number) {
  ctx.save()
  ctx.fillStyle = '#f6e8e6'
  ctx.fillRect(x, y, w, h)
  ctx.beginPath()
  ctx.rect(x, y, w, h)
  ctx.clip()
  ctx.strokeStyle = '#c2685c'
  ctx.lineWidth = 1
  for (let i = -h; i < w + h; i += 5) {
    ctx.beginPath()
    ctx.moveTo(x + i, y)
    ctx.lineTo(x + i - h, y + h)
    ctx.stroke()
  }
  ctx.restore()
}

function draw() {
  const cv = canvasEl.value
  const rows = plotted.value
  if (!cv || !rows.length) return
  const n = rows[0].cells.length
  if (!n) return
  const dpr = window.devicePixelRatio || 1
  const w = cv.clientWidth || 600
  const h = AXIS + rows.length * ROW
  cv.width = Math.max(1, Math.floor(w * dpr))
  cv.height = Math.max(1, Math.floor(h * dpr))
  cv.style.height = h + 'px'
  const ctx = cv.getContext('2d')
  if (!ctx) return
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, w, h)
  ctx.font = '11px sans-serif'
  ctx.fillStyle = '#8a8a8a'
  const ticks = 4
  for (let i = 0; i <= ticks; i++) {
    const idx = Math.min(n - 1, Math.round(((n - 1) * i) / ticks))
    const x = (idx / n) * w
    const text = tickLabel(rows[0].cells[idx].ts)
    ctx.fillText(text, Math.min(w - 52, Math.max(0, x - 14)), 14)
  }
  const cellW = w / n
  rows.forEach((m, ri) => {
    const y = AXIS + ri * ROW + 4
    const bh = ROW - 8
    const max = m.max_bytes || 0
    let i = 0
    const cells = m.cells
    while (i < cells.length) {
      const c = cells[i]
      let j = i + 1
      // Online cells keep their own shade. Gap and bridge collapse into runs
      // so a silence stays a visible band instead of a hairline of zeros.
      if (c.state !== 'online') {
        while (j < cells.length && cells[j].state === c.state) j++
      }
      let x = (i / n) * w
      let rw = ((j - i) / n) * w
      if (c.state === 'gap') {
        if (rw < 3) {
          x = Math.max(0, x - (3 - rw) / 2)
          rw = 3
        }
        paintGap(ctx, x, y, rw, bh)
      } else if (c.state === 'bridge' || !c.known) {
        ctx.fillStyle = c.state === 'bridge' ? BRIDGE : UNKNOWN
        ctx.fillRect(x, y, Math.max(rw, 0.6), bh)
      } else {
        const t = max > 0 ? (c.bytes || 0) / max : 0
        ctx.fillStyle = t <= 0 ? IDLE : mix(IDLE, BUSY, 0.18 + 0.82 * t)
        ctx.fillRect(x, y, Math.max(rw, cellW), bh)
      }
      i = j
    }
  })
}

function hit(ev: MouseEvent) {
  const cv = canvasEl.value
  const rows = plotted.value
  if (!cv || !rows.length) return null
  const rect = cv.getBoundingClientRect()
  const x = ev.clientX - rect.left
  const y = ev.clientY - rect.top
  if (y < AXIS) return null
  const ri = Math.floor((y - AXIS) / ROW)
  if (ri < 0 || ri >= rows.length) return null
  const cells = rows[ri].cells
  const i = Math.min(cells.length - 1, Math.max(0, Math.floor((x / rect.width) * cells.length)))
  return { machine: rows[ri], cell: cells[i], x, y }
}

function detailFor(m: Strip, c: Cell) {
  if (c.state === 'gap') return '探针没有上报。这不是零流量。'
  if (c.state === 'bridge') return '这一档没有采样，间隔还没到中断，流量未知。'
  if (!c.known) return '探针有上报，但这档算不出流量，不当成零流量。'
  if (!c.bytes) return '探针在报，几乎没有流量。'
  return '探针在报，这一档流量 ' + fmtBytes(c.bytes)
}

function onMove(ev: MouseEvent) {
  const h = hit(ev)
  if (!h) {
    tip.value = null
    return
  }
  const rectW = canvasEl.value?.clientWidth || 0
  tip.value = {
    x: Math.min(h.x + 12, Math.max(8, rectW - 180)),
    y: Math.max(8, h.y - 48),
    name: h.machine.name,
    when: fmtDateTime(h.cell.ts),
    detail: detailFor(h.machine, h.cell),
  }
}

function open(m: Strip) {
  if (!tl.value) return
  router.push({
    name: 'admin-monitor-detail',
    params: { id: m.server_id },
    hash: '#traffic-status',
    query: { from: String(tl.value.from), to: String(tl.value.to) },
  })
}

function onClick(ev: MouseEvent) {
  const h = hit(ev)
  if (h) open(h.machine)
}

async function load() {
  const span = presets.find(r => r.v === preset.value)?.sec || 24 * 3600
  const now = Math.floor(Date.now() / 1000)
  loading.value = true
  try {
    tl.value = await apiGet<Timeline>(`/api/admin/monitor/health-timeline?from=${now - span}&to=${now}`)
    ready.value = true
    await nextTick()
    draw()
  } catch (e: any) {
    ready.value = true
    message.error(e?.message || '查询机器健康失败')
  } finally {
    loading.value = false
  }
}

let resizeObs: ResizeObserver | null = null
watch(plotted, () => nextTick(draw))
watch(canvasEl, (el) => {
  resizeObs?.disconnect()
  resizeObs = null
  if (el && typeof ResizeObserver !== 'undefined') {
    resizeObs = new ResizeObserver(() => draw())
    resizeObs.observe(el.parentElement || el)
  }
})

onMounted(() => { load() })
onUnmounted(() => resizeObs?.disconnect())
</script>

<style scoped>
.health-card { margin-bottom: 16px; }
.sec-title { font-weight: 650; font-size: 14px; }
.sec-note { font-size: 11.5px; color: var(--text-3); margin-left: 10px; font-weight: 400; }
.health-note { margin: 0 0 12px; font-size: 12px; color: var(--text-3); line-height: 1.55; }
.legend { display: flex; flex-wrap: wrap; gap: 12px 16px; margin-bottom: 8px; font-size: 12px; color: var(--text-2); }
.legend span { display: inline-flex; align-items: center; gap: 6px; }
.sw { width: 22px; height: 10px; border-radius: 2px; display: inline-block; }
.sw.idle { background: #e4eee8; border: 1px solid #c9ddd2; }
.sw.busy { background: #3f6b52; }
.sw.bridge { background: #efe6d4; border: 1px solid #e0d3b4; }
.sw.gap {
  background: repeating-linear-gradient(-45deg, #f6e8e6, #f6e8e6 2px, #c2685c 2px, #c2685c 3px);
}
.heat { display: flex; gap: 8px; align-items: flex-start; }
.labels { width: 132px; flex: 0 0 132px; }
.axis-spacer { height: 22px; }
.lab {
  display: flex; align-items: center; gap: 4px;
  height: 26px; width: 100%; padding: 0; border: 0; background: transparent;
  cursor: pointer; text-align: left; color: var(--text-1);
}
.lab-name { font-size: 12px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lab-tag { flex: 0 0 auto; font-size: 10px; color: var(--text-3); border: 1px solid var(--border); border-radius: 4px; padding: 0 4px; }
.plot { position: relative; flex: 1; min-width: 0; }
.heat-canvas { width: 100%; display: block; cursor: pointer; }
.tip {
  position: absolute; z-index: 2; pointer-events: none;
  background: var(--card, #fff); border: 1px solid var(--border); border-radius: 8px;
  padding: 6px 8px; font-size: 12px; color: var(--text-2);
  display: flex; flex-direction: column; gap: 2px; max-width: 220px;
  box-shadow: 0 4px 14px rgba(0,0,0,.06);
}
.tip b { color: var(--text-1); }
.empty-list { list-style: none; margin: 8px 0 0; padding: 0; }
.empty-list li { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-3); }
.linkish { width: auto; height: auto; }
</style>
