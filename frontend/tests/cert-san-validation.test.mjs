import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const certs = readFileSync(new URL('../src/views/AdminCerts.vue', import.meta.url), 'utf8')
const sb = readFileSync(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')

// #119: the backend now rejects SAN-less or non-covering certificates; the
// forms must say so up front instead of only surfacing the error on save.
test('paste dialog explains the SAN requirement', () => {
  assert.match(certs, /证书必须包含 SAN（subjectAltName）且覆盖所填域名/)
  assert.match(certs, /留空则自动取证书覆盖的名称（SAN）/)
  assert.doesNotMatch(certs, /留空则自动取证书 CN/)
})

test('inline TLS certificate field explains SAN/SNI coverage', () => {
  assert.match(sb, /证书需含 SAN 并覆盖上方 SNI（开启跳过校验时仅要求含 SAN）/)
})
