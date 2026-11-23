import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const singbox = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')

test('TLS form exposes ECH toggle, public name input, and server-side keygen (#107)', () => {
  assert.match(singbox, /启用 ECH/)
  assert.match(singbox, /ECH 公开名称/)
  assert.match(singbox, /onEchPublicNameChange/)
  assert.match(singbox, /\/api\/admin\/sb\/tls\/ech-keygen/)
  assert.match(singbox, /ech_key_token/)
  assert.match(singbox, /ech_enabled/)
  assert.doesNotMatch(singbox, /BEGIN ECH KEYS/)
})
