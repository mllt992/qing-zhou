import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const source = await readFile(new URL('../src/views/AdminUpstreams.vue', import.meta.url), 'utf8')
const monitor = await readFile(new URL('../src/views/Monitor.vue', import.meta.url), 'utf8')
const nodes = await readFile(new URL('../src/views/AdminNodes.vue', import.meta.url), 'utf8')
const layout = await readFile(new URL('../src/components/DashboardLayout.vue', import.meta.url), 'utf8')
const router = await readFile(new URL('../src/router/index.ts', import.meta.url), 'utf8')

test('upstream management page keeps provider queries separate and exposes no saved credentials', () => {
  assert.match(source, /OCI Usage API/)
  assert.match(source, /Cloudflare Analytics GraphQL/)
  assert.match(source, /\/api\/admin\/upstreams\/\$\{provider\}\/refresh/)
  assert.match(source, /private_key_set/)
  assert.match(source, /analytics_token_set/)
  assert.doesNotMatch(source, /v-model:value="ociForm\.private_key"[^>]+show-password/)
})

test('upstream management is above admin overview in operations navigation', () => {
  assert.ok(layout.indexOf("{ label: '上游管理'") < layout.indexOf("{ label: '管理概览'"))
  assert.match(router, /path: 'admin\/upstreams'/)
})

test('upstream cards persist drag order and the public monitor keeps balance data admin-only', () => {
  assert.match(source, /admin_upstream_balance_order/)
  assert.match(source, /draggable="true"/)
  assert.match(source, /handleProviderDrop/)
  assert.match(monitor, /auth\.isAdmin \? apiList<any>\('\/api\/admin\/monitor\/servers'\)/)
  assert.match(monitor, /\/api\/admin\/upstreams\/\$\{provider\}\/refresh/)
  assert.match(monitor, /handleUpstreamDrop/)
})

test('homepage balance card is independently gated from the upstream management page', () => {
  assert.match(source, /首页显示上游余额/)
  assert.match(source, /admin_upstream_balance_visible/)
  assert.match(source, /toggleHomepageVisible/)
  assert.match(source, /saved !== 'false' && configured/)
  assert.match(monitor, /homepageCards/)
  assert.match(monitor, /s\.name === UPSTREAM_BALANCE_CARD/)
  assert.match(monitor, /saved !== 'false' && configured/)
  assert.doesNotMatch(monitor, /s\.name === '面板本机' && upstreamBalanceVisible/)
})

test('node cards use drag-and-drop for the shared subscription order', () => {
  assert.match(nodes, /class="list-card node-sort-card"/)
  assert.match(nodes, /handleNodeDrop/)
  assert.match(nodes, /\/api\/admin\/nodes\/reorder/)
  assert.doesNotMatch(nodes, /前移（订阅\/列表更靠前）/)
  assert.doesNotMatch(nodes, /@click="moveNodeInGroup/)
})
