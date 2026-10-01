import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const source = await readFile(new URL('../src/views/AdminUpdate.vue', import.meta.url), 'utf8')

test('rollback clearly separates binary rollback from database recovery', () => {
  assert.match(source, /二进制回滚不等于数据库降级/)
  assert.match(source, /恢复快照会丢失快照之后的新增数据/)
  assert.match(source, /rollback\.snapshot/)
  assert.match(source, /下载对应快照/)
  assert.match(source, /没有可核验的对应快照/)
  assert.doesNotMatch(source, /旧版本会忽略多出来的列/)
  assert.doesNotMatch(source, /cp -f qingzhou\.prev/)
})

test('snapshot download is authenticated and guarded against repeated clicks', () => {
  assert.match(source, /apiDownload\(`/)
  assert.match(source, /encodeURIComponent\(snapshot\.id\)/)
  assert.match(source, /if \(downloadingSnapshot\.value\) return/)
  assert.match(source, /finally \{\s*downloadingSnapshot\.value = ''/)
  assert.match(source, /snapshotting: '创建数据库快照'/)
  assert.match(source, /source_revision/)
  assert.match(source, /snapshot\.sha256/)
})

test('offline restoration guidance covers WAL, encryption key and verification', () => {
  for (const text of ['QZ_SECRET_KEY', 'QZ_DB', '-wal', '-shm', 'PRAGMA integrity_check', '0600', '隔离环境']) {
    assert.ok(source.includes(text), `missing restore guidance: ${text}`)
  }
  assert.match(source, /失败则中止/)
})
