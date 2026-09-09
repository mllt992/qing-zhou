import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createPinia, setActivePinia } from 'pinia'
import ts from 'typescript'

const storage = new Map()
globalThis.localStorage = { getItem: k => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v), removeItem: k => storage.delete(k) }
const source = (await readFile(new URL('../src/stores/auth.ts', import.meta.url), 'utf8'))
  .replace("from 'pinia'", `from '${import.meta.resolve('pinia')}'`)
  .replace("from 'vue'", `from '${import.meta.resolve('vue')}'`)
  .replace("import { apiPost, apiGet } from '@/api'", 'const apiPost = (...args) => globalThis.authAPI.post(...args); const apiGet = (...args) => globalThis.authAPI.get(...args)')
const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
const { useAuthStore } = await import('data:text/javascript;base64,' + Buffer.from(js).toString('base64'))
const user = { id: 1, username: 'oauth_user', is_admin: false }

test.beforeEach(() => { storage.clear(); setActivePinia(createPinia()) })
test('reload restores an HttpOnly cookie session without a localStorage JWT', async () => {
  let reads = 0
  globalThis.authAPI = { get: async () => { reads++; return user } }
  const auth = useAuthStore()
  await Promise.all([auth.init(), auth.init()])
  assert.equal(reads, 1)
  assert.equal(auth.isLoggedIn, true)
  assert.equal(auth.token, '')
})
test('anonymous bootstrap clears stale credentials without issuing logout', async () => {
  storage.set('qz_token', 'expired')
  let posts = 0
  globalThis.authAPI = { get: async () => { throw Object.assign(new Error('unauthorized'), { status: 401 }) }, post: async () => { posts++ } }
  const auth = useAuthStore(); await auth.init()
  assert.equal(auth.isLoggedIn, false)
  assert.equal(posts, 0)
  assert.equal(storage.has('qz_token'), false)
})
test('OAuth callback discards an old bearer token before reading new cookie session', async () => {
  storage.set('qz_token', 'previous-account')
  const auth = useAuthStore()
  globalThis.authAPI = { get: async () => { assert.equal(auth.token, ''); return user }, post: async () => { throw new Error('must not revoke new cookie') } }
  await auth.loginFromCookie()
  assert.equal(auth.user.id, user.id)
  assert.equal(auth.loaded, true)
  assert.equal(storage.has('qz_token'), false)
})
test('callback errors do not claim successful authentication', async () => {
  globalThis.authAPI = { get: async () => { throw Object.assign(new Error('unavailable'), { status: 503 }) } }
  const auth = useAuthStore()
  await assert.rejects(auth.loginFromCookie(), /unavailable/)
  assert.equal(auth.isLoggedIn, false)
})
test('logout revokes cookie-only sessions', async () => {
  const paths = []
  globalThis.authAPI = { get: async () => user, post: async path => { paths.push(path) } }
  const auth = useAuthStore(); await auth.init(); auth.logout()
  assert.deepEqual(paths, ['/api/auth/logout'])
  assert.equal(auth.isLoggedIn, false)
})
