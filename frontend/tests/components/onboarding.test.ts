import { beforeEach, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import AdminOnboarding from '@/components/AdminOnboarding.vue'
import { useAuthStore } from '@/stores/auth'
import { apiGet } from '@/api'
vi.mock('@/api', () => ({ apiGet: vi.fn(), apiPost: vi.fn() }))
const state = (complete = false) => ({ complete, checked_at: 1, steps: [{ id: 'metering', title: '流量统计插件', detail: '缺少 with_v2ray_api', path: '/admin/servers', done: complete, optional: false, severity: complete ? 'success' : 'error' }] })
beforeEach(() => { localStorage.clear(); vi.resetAllMocks() })
async function render(path = '/admin') {
 const pinia = createPinia(); setActivePinia(pinia)
 useAuthStore().user = { id: 1, username: 'admin', email: '', email_verified: true, role: 'admin', is_admin: true, status: 'active', points: 0 }
 const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
 await router.push(path); await router.isReady()
 const wrapper = mount(AdminOnboarding, { global: { plugins: [pinia, router] } }); await flushPromises()
 return { wrapper, router }
}
it('fresh installs show actionable warnings and refreshing completion hides the card', async () => {
 vi.mocked(apiGet).mockResolvedValueOnce(state()).mockResolvedValueOnce(state(true))
 const { wrapper } = await render()
 expect(wrapper.text()).toContain('缺少 with_v2ray_api')
 expect(wrapper.find('.onboarding-error').exists()).toBe(true)
 await wrapper.findAll('button').find(b => b.text() === '刷新')!.trigger('click'); await flushPromises()
 expect(wrapper.find('ol').exists()).toBe(false)
 wrapper.unmount()
})
it('configured installations stay quiet while forced reopening and dismissing remain available', async () => {
 vi.mocked(apiGet).mockResolvedValue(state(true))
 const quiet = await render(); expect(quiet.wrapper.find('ol').exists()).toBe(false); quiet.wrapper.unmount()
 const { wrapper, router } = await render('/admin?checklist=1')
 expect(wrapper.text()).toContain('关键步骤已完成')
 await wrapper.findAll('button').find(b => b.text() === '关闭')!.trigger('click'); await flushPromises()
 expect(router.currentRoute.value.query.checklist).toBeUndefined()
 expect(localStorage.getItem('qz-onboarding-dismissed:1')).toBe('1')
 wrapper.unmount()
})
it('dismissal is remembered and unmount aborts an unfinished read', async () => {
 localStorage.setItem('qz-onboarding-dismissed:1', '1')
 let signal: AbortSignal|undefined
 vi.mocked(apiGet).mockImplementation((_path, options) => { signal = options?.signal as AbortSignal; return new Promise(() => {}) })
 const { wrapper } = await render()
 expect(wrapper.find('ol').exists()).toBe(false)
 wrapper.unmount(); expect(signal?.aborted).toBe(true)
})
