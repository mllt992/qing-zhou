import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const admin = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')

test('hysteria2 inbound form configures Realm and drops the placeholder (#103)', () => {
  assert.doesNotMatch(admin, /暂不提供表单/)
  for (const field of ['realm_server_url', 'realm_token', 'realm_id', 'realm_stun', 'realm_ip_version']) {
    assert.match(admin, new RegExp(field))
  }
  assert.match(admin, /realmSwitchDisabled/)
  assert.match(admin, /低于/)
  assert.match(admin, /Clash 订阅和分享链接不会包含此节点/)
})
