import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const page = await readFile(new URL('../src/views/AdminServers.vue', import.meta.url), 'utf8')

test('server form no longer offers a manual egress IPv4/IPv6 preference', () => {
  assert.doesNotMatch(page, /出站 IPv4\/IPv6 偏好/)
  assert.doesNotMatch(page, /egress_ip_strategy/)
  assert.doesNotMatch(page, /egressIPOptions/)
  assert.doesNotMatch(page, /prefer_ipv4/)
})

test('IPv6 subscription hint explains automatic source-family egress', () => {
  assert.match(page, /本机直出流量会按客户端连入的地址族自动优先走 IPv4 或 IPv6/)
  assert.match(page, /中转落地不受影响/)
  assert.match(page, /建议填字面量/)
})
