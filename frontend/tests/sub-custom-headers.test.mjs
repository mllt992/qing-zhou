import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const settings = await readFile(new URL('../src/views/AdminSettings.vue', import.meta.url), 'utf8')

test('subscription template settings expose custom response headers control', () => {
  assert.match(settings, /自定义订阅响应头/)
  assert.match(settings, /form\.sub_custom_headers/)
  assert.match(settings, /form\.sub_custom_headers \?\?= ''/)
  assert.match(settings, /Accept-Encoding: gzip/)
})
