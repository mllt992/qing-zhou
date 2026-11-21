import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')

test('inbound cards offer a TLS check for TCP TLS inbounds only (#118)', () => {
  assert.match(source, /\/api\/admin\/sb\/inbounds\/' \+ r\.id \+ '\/tls-check'/)
  assert.match(source, /const TLS_PROBE_QUIC = \['tuic', 'hysteria', 'hysteria2'\]/)
  assert.match(source, /v-if="r\.tls_id && !TLS_PROBE_QUIC\.includes\(r\.type\)"/)
  assert.match(source, /tlsStatusText/)
})
