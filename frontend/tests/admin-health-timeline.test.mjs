import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const overview = await readFile(new URL('../src/views/AdminOverview.vue', import.meta.url), 'utf8')
const dash = await readFile(new URL('../src/views/UserDashboard.vue', import.meta.url), 'utf8')
const strip = await readFile(new URL('../src/components/MachineHealthStrip.vue', import.meta.url), 'utf8')
const detail = await readFile(new URL('../src/views/AdminMonitorDetail.vue', import.meta.url), 'utf8')
const route = await readFile(new URL('../../internal/api/router.go', import.meta.url), 'utf8')

test('admin overview leads with the multi-machine health strip', () => {
  const head = overview.indexOf('管理概览')
  const stripAt = overview.indexOf('<MachineHealthStrip')
  const kpi = overview.indexOf('<!-- KPI -->')
  assert.ok(head > 0 && stripAt > head && kpi > stripAt)
  assert.match(overview, /import MachineHealthStrip/)
})

test('the console an admin lands on shows the same strip only for admins', () => {
  const head = dash.indexOf('控制台')
  const stripAt = dash.indexOf('<MachineHealthStrip v-if="auth.isAdmin"')
  const kpi = dash.indexOf('<!-- 核心指标 -->')
  assert.ok(head > 0 && stripAt > head && kpi > stripAt)
})

test('strip color is probe presence and traffic is only a shade', () => {
  assert.match(strip, /还没有采样/)
  assert.match(strip, /探针中断/)
  assert.match(strip, /这不是零流量/)
  assert.match(strip, /几乎没流量/)
  assert.match(strip, /不报延迟和丢包/)
  assert.match(strip, /health-timeline/)
  assert.match(strip, /近 24 小时/)
  assert.match(strip, /近 30 天/)
  // A gap is hatched, not filled with the idle color.
  assert.match(strip, /paintGap/)
  assert.equal(strip.includes('state === \'gap\' ? IDLE'), false)
})

test('clicking a strip opens that machine traffic status for the same range', () => {
  assert.match(strip, /name: 'admin-monitor-detail'/)
  assert.match(strip, /hash: '#traffic-status'/)
  assert.match(strip, /from: String\(tl\.value\.from\)/)
  assert.match(strip, /to: String\(tl\.value\.to\)/)
  assert.match(detail, /function applyStatusQuery/)
  assert.match(detail, /statusPreset\.value = 'custom'/)
  assert.match(detail, /#traffic-status/)
})

test('timeline route is admin-only and stored-metrics only', () => {
  assert.match(route, /\/api\/admin\/monitor\/health-timeline/)
})
