import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const comp = await readFile(new URL('../src/components/AdminComplianceCheck.vue', import.meta.url), 'utf8')
const page = await readFile(new URL('../src/views/AdminServers.vue', import.meta.url), 'utf8')

test('servers page offers the compliance check button', () => {
  assert.match(page, /<AdminComplianceCheck \/>/)
  assert.match(comp, /apiPost\('\/api\/admin\/compliance\/check'\)/)
})

test('results show severity, explanation and fix; errors are toasts', () => {
  for (const t of ['高危', '中危', '低危', '提示', '建议：', 'f.detail', 'live_note']) assert.ok(comp.includes(t), t)
  assert.match(comp, /message\.error\('检测失败：'/)
  assert.doesNotMatch(comp, /<n-alert/)
})
