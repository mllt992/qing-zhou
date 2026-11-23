import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const router = await readFile(new URL('../src/router/index.ts', import.meta.url), 'utf8')
const layout = await readFile(new URL('../src/components/DashboardLayout.vue', import.meta.url), 'utf8')
const header = await readFile(new URL('../src/components/AppHeader.vue', import.meta.url), 'utf8')
const singbox = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')
const servers = await readFile(new URL('../src/views/AdminServers.vue', import.meta.url), 'utf8')
const setup = await readFile(new URL('../src/views/AdminSetup.vue', import.meta.url), 'utf8')

test('setup wizard is registered as a standalone admin route and linked in navigation', () => {
  assert.match(router, /path:\s*'admin\/setup'/)
  assert.match(layout, /key:\s*'\/admin\/setup'/)
  assert.match(layout, /'\/admin\/setup':\s*'配置向导'/)
  assert.match(header, /key:\s*'\/admin\/setup'/)
})

test('servers and sing-box views offer direct entries into the setup wizard for create and edit', () => {
  assert.match(singbox, /\/admin\/setup/)
  assert.match(singbox, /向导编辑/)
  assert.match(servers, /\/admin\/setup/)
  assert.match(servers, /向导配置/)
})

test('setup wizard supports switching between create and edit modes directly on the standalone page', () => {
  assert.match(setup, /新建配置/)
  assert.match(setup, /编辑已有入站/)
  assert.match(setup, /existingUpstreamId/)
  assert.match(setup, /existingEgressId/)
})
