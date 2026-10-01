import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { NDialogProvider, NMessageProvider } from 'naive-ui'
import AdminSingbox from '@/views/AdminSingbox.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), post: vi.fn(), put: vi.fn(), remove: vi.fn() }))
vi.mock('@/api', () => ({ apiList: api.list, apiGet: api.get, apiGetRaw: api.get, apiPost: api.post, apiPut: api.put, apiDelete: api.remove }))
const publicRow = { id: 1, server_id: 0, name: 'TLS fixture', mode: 'tls', server_json: '{"server_name":"fixture.invalid"}', client_json: '{}' }
const fullRow = { ...publicRow, server_json: '{"server_name":"fixture.invalid","certificate":"certificate-fixture","key":"private-key-fixture"}' }
let wrapper: VueWrapper
function editor() { return wrapper.findComponent(AdminSingbox).vm as any }
async function button(text: string) {
  const match = wrapper.findAll('button').find(button => button.text() === text)
  expect(match, `button ${text}`).toBeTruthy()
  await match!.trigger('click'); await flushPromises()
}

beforeEach(async () => {
  vi.clearAllMocks()
  api.list.mockImplementation((path: string) => Promise.resolve(path === '/api/admin/sb/tls' ? [publicRow] : []))
  api.get.mockResolvedValue({})
  wrapper = mount(defineComponent({ components: { AdminSingbox, NDialogProvider, NMessageProvider }, template: '<NMessageProvider><NDialogProvider><AdminSingbox /></NDialogProvider></NMessageProvider>' }), { attachTo: document.body })
  await flushPromises()
})
afterEach(() => { wrapper.unmount(); document.body.innerHTML = '' })

describe('TLS private detail loading', () => {
  it('keeps ordinary list loading prompt-free and requests the complete row for editing', async () => {
    expect(api.post).not.toHaveBeenCalled()
    api.post.mockResolvedValueOnce(fullRow)
    await button('编辑')
    expect(api.post.mock.calls[0][0]).toBe('/api/admin/sb/tls/1/export')
    expect(editor().showTls).toBe(true)
    expect(editor().te.key).toBe('private-key-fixture')
    expect(editor().te.certificate).toBe('certificate-fixture')
    api.put.mockResolvedValueOnce({})
    await editor().saveTls(); await flushPromises()
    expect(api.put.mock.calls[0][0]).toBe('/api/admin/sb/tls/cert/1')
    expect(api.put.mock.calls[0][1].key).toBe('private-key-fixture')
  })

  it('cancellation never opens an editor with empty private fields or saves a row', async () => {
    api.post.mockRejectedValueOnce(new DOMException('cancelled', 'AbortError'))
    await button('编辑')
    expect(editor().showTls).toBe(false)
    expect(api.put).not.toHaveBeenCalled()
  })

  it('a newer Add TLS action cancels an old detail load and cannot be overwritten by it', async () => {
    let finish!: (value: typeof fullRow) => void
    api.post.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    await button('编辑')
    const signal = api.post.mock.calls[0][2].signal as AbortSignal
    await button('添加 TLS')
    expect(signal.aborted).toBe(true)
    finish(fullRow); await flushPromises()
    expect(editor().showTls).toBe(true)
    expect(editor().te.id).toBe(0)
    expect(editor().te.key).toBe('')
  })

  it('cloning also loads full details and clears only the row identity', async () => {
    api.post.mockResolvedValueOnce(fullRow)
    await button('克隆')
    expect(api.post.mock.calls[0][0]).toBe('/api/admin/sb/tls/1/export')
    expect(editor().te.id).toBe(0)
    expect(editor().te.key).toBe('private-key-fixture')
    expect(editor().te.name).toContain('副本')
  })
})
