import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const serversPage = await readFile(new URL('../src/views/AdminServers.vue', import.meta.url), 'utf8')
const settingsPage = await readFile(new URL('../src/views/AdminSettings.vue', import.meta.url), 'utf8')
const singboxPage = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')

test('servers page exposes Cloudflare subdomain modal and domain fields', () => {
  assert.match(serversPage, /openDnsModal/)
  assert.match(serversPage, /\/admin\/cf\/zones/)
  assert.match(serversPage, /\/api\/admin\/servers\/\$\{[^}]+\}\/dns/)
  assert.match(serversPage, /form\.domain/)
  assert.match(serversPage, /form\.domain_v6/)
  assert.match(serversPage, /灰云/)
})

test('settings page offers Cloudflare token verification action', () => {
  assert.match(settingsPage, /\/admin\/settings\/test-cf-token/)
  assert.match(settingsPage, /测试 Token/)
})

test('REALITY SNI presets exclude CDN/Cloudflare domains and warn users', () => {
  const presetBlock = singboxPage.match(/const sniPresets\s*=\s*\[[\s\S]*?\]/)
  assert.ok(presetBlock, 'expected sniPresets array')
  assert.doesNotMatch(presetBlock[0], /cloudflare\.com|icloud\.com|apple\.com|tesla\.com/)
  assert.match(singboxPage, /请勿选择托管在/)
})

test('mixed inbound with self-signed TLS surfaces a warning instead of a misleading green badge', () => {
  assert.match(singboxPage, /editingMixedTlsSelfSigned/)
  assert.match(singboxPage, /self-signed certificate/)
})
