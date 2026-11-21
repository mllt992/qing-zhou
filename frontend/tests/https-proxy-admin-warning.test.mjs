import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const admin = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')
const user = await readFile(new URL('../src/views/UserSub.vue', import.meta.url), 'utf8')

test('admin inbound cards and editor surface the HTTPS proxy certificate/address warning (#115)', () => {
  assert.match(admin, /r\.tls_warning/)
  assert.match(admin, /证书不覆盖节点地址/)
  assert.match(admin, /editingInboundWarning/)
})

test('user page explains HTTPS proxy trust without changing the copied URL format (#115)', () => {
  assert.match(user, /httpsProxyHints\(p\)/)
  assert.match(user, /formatFingerprint\(p\.sha256\)/)
  assert.match(user, /TLS 服务器名/)
  // The ready-made URL stays scheme://user:pass@host:port.
  assert.match(user, /return `\$\{scheme\}:\/\/\$\{cred\}\$\{host\}:\$\{p\.port\}`/)
})
