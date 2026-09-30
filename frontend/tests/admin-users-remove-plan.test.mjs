import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const source = await readFile(new URL('../src/views/AdminUsers.vue', import.meta.url), 'utf8')

function removePlanFn() {
  const start = source.indexOf('function removePlan')
  const end = source.indexOf('// 按份加减流量', start)
  assert.ok(start > 0 && end > start)
  return source.slice(start, end)
}

test('successful plan removal closes the confirm before refreshing the lists', () => {
  const fn = removePlanFn()
  const destroyAt = fn.indexOf('d.destroy()')
  const refreshAt = fn.indexOf('loadPlans(u.id)')
  assert.ok(destroyAt > 0, 'success path must close the dialog')
  assert.ok(refreshAt > destroyAt, 'list refresh must not gate closing the dialog')
  assert.match(fn, /message\.success\(isPool \? '流量包已清空' : '套餐已移除'\)/)
  // The delete failure path keeps the dialog open. A later refresh failure must not.
  const failReturn = fn.indexOf('return false')
  assert.ok(failReturn > 0 && failReturn < destroyAt)
  assert.match(fn, /message\.error\(e\?\.message \|\| \(isPool \? '清空失败' : '移除失败'\)\)/)
})

test('plan removal confirm is only the nested one inside the plans modal', () => {
  const fn = removePlanFn()
  assert.match(fn, /确认移除未生效套餐/)
  assert.match(fn, /apiDelete\(`\/api\/admin\/users\/\$\{u\.id\}\/plans\/\$\{p\.id\}`\)/)
  const plansModal = source.indexOf('v-model:show="showPlans"')
  const removeCall = source.indexOf('@remove="removePlan(p)"')
  assert.ok(plansModal > 0 && removeCall > plansModal)
  // Delete-user / reset-credentials confirms are not this bug.
  assert.equal(source.includes("title: '确认删除用户'"), true)
  const deleteFn = source.slice(source.indexOf('function handleDelete'))
  assert.equal(deleteFn.includes('d.destroy()'), false)
})
