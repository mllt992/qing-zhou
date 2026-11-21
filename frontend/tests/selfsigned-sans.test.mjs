import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const certs = await readFile(new URL('../src/views/AdminCerts.vue', import.meta.url), 'utf8')

test('certificate centre self-signed form sends extra SANs (#117)', () => {
  assert.match(certs, /附加 SAN/)
  assert.match(certs, /sans: splitSans\(self\.sans\)/)
  const m = certs.match(/function splitSans\(v: string\): string\[\] \{\n\s+return (.+)\n\}/)
  assert.ok(m, 'splitSans present')
  const splitSans = new Function('v', 'return ' + m[1])
  assert.deepEqual(splitSans('1.2.3.4, 2001:db8::1，a.example  b.example、c'), ['1.2.3.4', '2001:db8::1', 'a.example', 'b.example', 'c'])
  assert.deepEqual(splitSans(''), [])
})
