import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const monitor = await readFile(new URL('../src/views/AdminMonitor.vue', import.meta.url), 'utf8')
const homepage = await readFile(new URL('../src/views/Monitor.vue', import.meta.url), 'utf8')
const settings = await readFile(new URL('../src/views/AdminSettings.vue', import.meta.url), 'utf8')
const overview = await readFile(new URL('../src/views/AdminOverview.vue', import.meta.url), 'utf8')
const dash = await readFile(new URL('../src/views/UserDashboard.vue', import.meta.url), 'utf8')
const strip = await readFile(new URL('../src/components/MachineHealthStrip.vue', import.meta.url), 'utf8')
const detail = await readFile(new URL('../src/views/AdminMonitorDetail.vue', import.meta.url), 'utf8')
const route = await readFile(new URL('../../internal/api/router.go', import.meta.url), 'utf8')

test('health belongs to server monitoring and never loads on admin overview', () => {
  assert.doesNotMatch(overview, /MachineHealthStrip|AdminOnboarding/)
  assert.match(monitor, /<MachineHealthStrip \/>/)
  assert.match(monitor, /import MachineHealthStrip/)
})

test('homepage and console health require both administrator status and explicit opt-in', () => {
  for (const source of [homepage, dash]) {
    assert.match(source, /<MachineHealthStrip v-if="auth.isAdmin && config.config.homepage_machine_health"/)
  }
  assert.doesNotMatch(dash, /AdminOnboarding/)
  assert.match(settings, /v-model:value="form.homepage_machine_health" checked-value="true" unchecked-value="false"/)
})

test('deployment checklist has a dedicated lazy-mounted settings section', () => {
  assert.match(settings, /id: 'settings-onboarding', label: '部署检查清单'/)
  assert.match(settings, /<AdminOnboarding v-if="activeSectionId === 'settings-onboarding'"/)
  assert.doesNotMatch(settings, /重新打开部署检查清单/)
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
