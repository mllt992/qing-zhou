import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { apiDownload, apiPost } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { cancelStepUpPrompt, cancelStepUpRequests, finishStepUpPrompt, stepUpPrompt } from '@/api/reauth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import App from '@/App.vue'

const backupScope = 'admin:backup'
const updateScope = 'admin:update'
const challenge = (scope = updateScope) => new Response(JSON.stringify({ code: 403, msg: '需要密码二次验证', data: { error: 'step_up_required', scope } }), { status: 403 })
const success = (data: unknown = { started: true }) => new Response(JSON.stringify({ code: 0, data }))
const proofResponse = (scope = updateScope) => success({ proof: 'test-proof', scope, expires_in: 300 })
let wrapper: VueWrapper | undefined
let fetchMock: ReturnType<typeof vi.fn>
let pinia: ReturnType<typeof createPinia>

function mountDialog() { wrapper = mount(StepUpDialog, { attachTo: document.body, global: { plugins: [pinia] } }); return wrapper }
async function fillAndSubmit(password = 'current-password-fixture') {
  await flushPromises()
  const input = document.querySelector<HTMLInputElement>('#step-up-password')!
  expect(input).toBeTruthy()
  input.value = password
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
  document.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flushPromises()
}
function complete(scope = updateScope) {
  expect(stepUpPrompt.value?.scope).toBe(scope)
  finishStepUpPrompt(stepUpPrompt.value!.id, { proof: 'test-proof', scope, expires_in: 300 })
}
function proofHeader(call: number) { return (fetchMock.mock.calls[call][1].headers as Headers).get('X-QZ-Step-Up') }

beforeEach(() => {
  pinia = createPinia(); setActivePinia(pinia)
  const auth = useAuthStore()
  auth.token = 'session-token'
  auth.user = { id: 1, username: 'admin', role: 'admin', is_admin: true, email: '', email_verified: true, status: 'active', points: 0 }
  fetchMock = vi.fn(); vi.stubGlobal('fetch', fetchMock)
  cancelStepUpRequests(true)
})
afterEach(() => {
  wrapper?.unmount(); wrapper = undefined
  cancelStepUpRequests(true)
  vi.restoreAllMocks(); vi.unstubAllGlobals()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('password confirmation dialog and shared API path', () => {
  it('confirms and retries a binary download with the scoped proof, never a URL secret', async () => {
    fetchMock.mockResolvedValueOnce(challenge(backupScope)).mockResolvedValueOnce(proofResponse(backupScope)).mockResolvedValueOnce(new Response('SQLite fixture', { headers: { 'Content-Disposition': 'attachment; filename="safe-backup.db"' } }))
    const createURL = vi.fn(() => 'blob:test-download')
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: createURL, revokeObjectURL: vi.fn() }))
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    mountDialog()
    const download = apiDownload('/api/admin/backup', 'fallback.db')
    await fillAndSubmit()
    await download
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls.map(call => call[0])).toEqual(['/api/admin/backup', '/api/user/reauth', '/api/admin/backup'])
    expect(proofHeader(0)).toBeNull(); expect(proofHeader(1)).toBeNull(); expect(proofHeader(2)).toBe('test-proof')
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ method: 'password', password: 'current-password-fixture', scope: backupScope })
    expect(createURL).toHaveBeenCalledOnce(); expect(click).toHaveBeenCalledOnce()
    expect(stepUpPrompt.value).toBeNull()
  })

  it('reuses a scoped proof for repeated actions, but asks for a different scope', async () => {
    fetchMock.mockResolvedValueOnce(challenge()).mockResolvedValueOnce(success()).mockResolvedValueOnce(success()).mockResolvedValueOnce(challenge('admin:cert-export')).mockResolvedValueOnce(success())
    const first = apiPost('/api/admin/update/apply'); await flushPromises(); complete(); await first
    await apiPost('/api/admin/update/apply')
    expect(proofHeader(2)).toBe('test-proof'); expect(stepUpPrompt.value).toBeNull()
    const second = apiPost('/api/admin/certs/1/export'); await flushPromises()
    expect(fetchMock.mock.calls[3][1].method).toBe('POST')
    complete('admin:cert-export'); await second
  })

  it('cancels without replaying and permits a clean new confirmation', async () => {
    fetchMock.mockResolvedValueOnce(challenge()).mockResolvedValueOnce(challenge()).mockResolvedValueOnce(proofResponse()).mockResolvedValueOnce(success())
    mountDialog()
    const first = apiPost('/api/admin/update/rollback').catch(error => error)
    await flushPromises()
    const firstID = stepUpPrompt.value!.id
    const cancel = [...document.querySelectorAll('button')].find(button => button.textContent?.trim() === '取消')!
    cancel.click(); await flushPromises()
    expect((await first).name).toBe('AbortError')
    expect(fetchMock).toHaveBeenCalledOnce()
    const second = apiPost('/api/admin/update/rollback'); await flushPromises()
    expect(stepUpPrompt.value!.id).not.toBe(firstID)
    expect(document.querySelector<HTMLInputElement>('#step-up-password')!.value).toBe('')
    await fillAndSubmit(); await second
  })

  it('shows wrong password and rate-limit errors without logging out, and blocks repeated submits', async () => {
    let finish!: (value: Response) => void
    fetchMock.mockResolvedValueOnce(challenge()).mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve })).mockResolvedValueOnce(new Response(JSON.stringify({ msg: '请稍后再试' }), { status: 429 }))
    mountDialog()
    const action = apiPost('/api/admin/update/apply').catch(error => error)
    await fillAndSubmit('wrong')
    document.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises(); expect(fetchMock).toHaveBeenCalledTimes(2)
    finish(new Response(JSON.stringify({ msg: '身份验证失败' }), { status: 403 }))
    await flushPromises()
    expect(document.querySelector('[role="alert"]')!.textContent).toContain('身份验证失败')
    expect(useAuthStore().token).toBe('session-token')
    expect(document.querySelector<HTMLInputElement>('#step-up-password')!.value).toBe('')
    await fillAndSubmit('wrong-again')
    expect(document.querySelector('[role="alert"]')!.textContent).toContain('请稍后再试')
    expect(useAuthStore().token).toBe('session-token')
    cancelStepUpPrompt(stepUpPrompt.value!.id); await action
  })

  it('closing during reauth rejects the pending action and ignores a late proof', async () => {
    let finish!: (value: Response) => void
    fetchMock.mockResolvedValueOnce(challenge()).mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
    mountDialog()
    const action = apiPost('/api/admin/update/apply').catch(error => error)
    await fillAndSubmit()
    cancelStepUpPrompt(stepUpPrompt.value!.id)
    finish(proofResponse()); await flushPromises()
    expect((await action).name).toBe('AbortError')
    expect(stepUpPrompt.value).toBeNull(); expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('an aborted caller dismisses its prompt and cannot perform the operation', async () => {
    fetchMock.mockResolvedValueOnce(challenge())
    const controller = new AbortController()
    const action = apiPost('/api/admin/update/apply', undefined, { signal: controller.signal }).catch(error => error)
    await flushPromises(); expect(stepUpPrompt.value).not.toBeNull()
    controller.abort(); await flushPromises()
    expect((await action).name).toBe('AbortError'); expect(stepUpPrompt.value).toBeNull()
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('navigation, Back and Forward dismiss the global modal without replaying old actions', async () => {
    fetchMock.mockImplementation((path: string) => Promise.resolve(path === '/api/config' ? success({}) : challenge()))
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<main>Page</main>' } }] })
    await router.push('/admin'); await router.isReady()
    wrapper = mount(App, { attachTo: document.body, global: { plugins: [pinia, router] } })
    await flushPromises()
    const first = apiPost('/api/admin/update/apply').catch(error => error)
    await flushPromises(); expect(stepUpPrompt.value).not.toBeNull()
    await router.push('/account'); await flushPromises()
    expect((await first).name).toBe('AbortError'); expect(stepUpPrompt.value).toBeNull()
    router.back(); await flushPromises()
    router.forward(); await flushPromises()
    expect(stepUpPrompt.value).toBeNull()
    expect(fetchMock.mock.calls.filter(call => call[0] === '/api/admin/update/apply')).toHaveLength(1)
  })

  it('asks again after proof expiry and never stores proofs persistently', async () => {
    let now = 1_000_000
    vi.spyOn(Date, 'now').mockImplementation(() => now)
    fetchMock.mockResolvedValueOnce(challenge()).mockResolvedValueOnce(success()).mockResolvedValueOnce(challenge()).mockResolvedValueOnce(success())
    const first = apiPost('/api/admin/update/apply'); await flushPromises(); complete(); await first
    now += 301_000
    const expired = apiPost('/api/admin/update/apply'); await flushPromises()
    expect(proofHeader(2)).toBeNull(); expect(stepUpPrompt.value).not.toBeNull()
    complete(); await expired
    expect(localStorage.getItem('test-proof')).toBeNull()
    expect([...Array(localStorage.length)].map((_, index) => localStorage.getItem(localStorage.key(index)!)).join(' ')).not.toContain('test-proof')
  })

  it('rejects unrecognized proof scopes without displaying a password prompt', async () => {
    fetchMock.mockResolvedValueOnce(challenge('unexpected:scope'))
    await expect(apiPost('/api/admin/update/apply')).rejects.toThrow('不支持的二次验证范围')
    expect(stepUpPrompt.value).toBeNull(); expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('never retries a second rejected proof or ordinary network errors', async () => {
    fetchMock.mockResolvedValueOnce(challenge()).mockResolvedValueOnce(challenge())
    const action = apiPost('/api/admin/update/apply').catch(error => error)
    await flushPromises(); complete()
    expect((await action).status).toBe(403)
    expect(fetchMock).toHaveBeenCalledTimes(2); expect(stepUpPrompt.value).toBeNull()
    fetchMock.mockRejectedValueOnce(new TypeError('offline'))
    await expect(apiPost('/api/admin/update/apply')).rejects.toThrow('offline')
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('invalidates cached proof after password change and logout', async () => {
    fetchMock.mockResolvedValueOnce(challenge()).mockResolvedValueOnce(success()).mockResolvedValueOnce(success()).mockResolvedValueOnce(challenge())
    const first = apiPost('/api/admin/update/apply'); await flushPromises(); complete(); await first
    await apiPost('/api/user/password', { old_password: 'old', new_password: 'new' })
    const afterChange = apiPost('/api/admin/update/apply').catch(error => error)
    await flushPromises(); expect(proofHeader(3)).toBeNull(); expect(stepUpPrompt.value).not.toBeNull()
    useAuthStore().logout(true)
    expect((await afterChange).name).toBe('AbortError'); expect(stepUpPrompt.value).toBeNull()
  })

  it('coalesces simultaneous same-scope prompts while keeping caller cancellation independent', async () => {
    fetchMock.mockResolvedValueOnce(challenge()).mockResolvedValueOnce(challenge()).mockResolvedValueOnce(success())
    const controller = new AbortController()
    const first = apiPost('/api/admin/update/apply', {}, { signal: controller.signal }).catch(error => error)
    const second = apiPost('/api/admin/update/rollback')
    await flushPromises()
    const id = stepUpPrompt.value!.id
    controller.abort(); await flushPromises()
    expect(stepUpPrompt.value!.id).toBe(id)
    complete(); await second
    expect((await first).name).toBe('AbortError')
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })
})
