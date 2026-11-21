import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('../src/views/AdminSingbox.vue', import.meta.url), 'utf8')

test('picking a managed certificate sets insecure from whether it is self-signed (#116)', () => {
  const fn = source.slice(source.indexOf('function onCertPick'), source.indexOf('// --- SNI 延迟测试'))
  assert.match(fn, /te\.insecure = !!c\.self_signed/)
})

test('generating an inline self-signed certificate enables insecure (#116)', () => {
  const fn = source.slice(source.indexOf('async function genSelfSigned'), source.indexOf('async function genKeys'))
  assert.match(fn, /te\.insecure = true/)
})
