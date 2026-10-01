import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'

async function importTS(path) {
  const source = (await readFile(new URL(path, import.meta.url), 'utf8'))
    .replace("import { useConfigStore } from '@/stores/config'", 'const useConfigStore = () => ({ config: { points_per_cny: 20 } })')
  return import('data:text/javascript;base64,' + Buffer.from(ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText).toString('base64'))
}
const plan = await importTS('../src/utils/plan.ts')
const fmt = await importTS('../src/utils/format.ts')

test('queued plans keep queue status even when old quota/expiry fields look exhausted', () => {
  const queued = { status: 'queued', expiry_at: 1, traffic_limit: 100, used: 100 }
  assert.deepEqual(plan.planStatusMeta(queued), { label: '排队中', type: 'info' })
  assert.equal(plan.planSortKey(queued), 1)
  assert.equal(plan.planTimeText(queued, String), '前一份用完后生效')
  assert.equal(plan.planTimeText({ ...queued, activate_by: 100 }, String), '预计 100 前生效')
})
test('plan state distinguishes finite/exhausted, unlimited, and expired plans', () => {
  assert.equal(plan.planStatusMeta({ status: 'active', traffic_limit: 0, used: 100 }).label, '使用中')
  assert.equal(plan.planStatusMeta({ status: 'active', traffic_limit: 100, used: 100 }).label, '已用尽')
  assert.equal(plan.planStatusMeta({ status: 'active', expiry_at: 1 }).label, '已过期')
  assert.equal(plan.planTimeText({ status: 'active', expiry_at: 0 }, String), '不过期')
})
test('traffic/points formatting respects boundaries and configured exchange rate', () => {
  assert.equal(fmt.fmtBytes(undefined), '0 B')
  assert.equal(fmt.fmtBytes(1024), '1.00 KB')
  assert.equal(fmt.fmtBytes(1024 ** 3), '1.00 GB')
  assert.equal(fmt.pct(120, 100), 100)
  assert.equal(fmt.pct(120, 0), 0)
  assert.equal(fmt.yuan(100), '≈¥5')
  assert.equal(fmt.yuan(100, 10), '≈¥10')
})
