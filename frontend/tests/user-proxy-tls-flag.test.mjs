import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const src = readFileSync(new URL('../src/views/UserSub.vue', import.meta.url), 'utf8')

// #120: whether a proxy is HTTPS comes only from the backend's `tls` flag, which
// now follows the same rule as subscription links (disabled profile → plain,
// inline options.tls → HTTPS). The page must not infer it from anything else.
test('HTTPS label and copy buttons are gated on p.tls only', () => {
  assert.match(src, /p\.tls \? 'HTTPS' : 'HTTP \/ SOCKS5'/)
  assert.match(src, /<template v-if="p\.tls">\s*<n-button[^>]*@click="copy\(proxyUrl\(p, 'https'\)\)"/)
  assert.doesNotMatch(src, /tls_id/)
})
