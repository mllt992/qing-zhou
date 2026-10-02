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
async function render(path = '/admin/settings?section=onboarding') {
 const pinia = createPinia(); setActivePinia(pinia)
 useAuthStore().user = { id: 1, username: 'admin', email: '', email_verified: true, role: 'admin', is_admin: true, status: 'active', points: 0 }
 const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
 await router.push(path); await router.isReady()
 const wrapper = mount(AdminOnboarding, { global: { plugins: [pinia, router] } }); await flushPromises()
 return { wrapper, router }
}
it('shows actionable warnings and retains completed steps after refresh', async () => {
 vi.mocked(apiGet).mockResolvedValueOnce(state()).mockResolvedValueOnce(state(true))
 const { wrapper } = await render()
 expect(wrapper.text()).toContain('缺少 with_v2ray_api')
 expect(wrapper.find('.onboarding-error').exists()).toBe(true)
 await wrapper.findAll('button').find(b => b.text() === '刷新')!.trigger('click'); await flushPromises()
 expect(wrapper.find('ol').exists()).toBe(true)
 expect(wrapper.text()).toContain('关键步骤已完成')
 expect(wrapper.find('.onboarding-error').exists()).toBe(false)
 wrapper.unmount()
})
it('completed or previously dismissed checklists remain available in settings', async () => {
 localStorage.setItem('qz-onboarding-dismissed:1', '1')
 vi.mocked(apiGet).mockResolvedValue(state(true))
 const { wrapper } = await render()
 expect(wrapper.text()).toContain('关键步骤已完成')
 expect(wrapper.find('ol').exists()).toBe(true)
 expect(wrapper.findAll('button').some(b => b.text() === '关闭')).toBe(false)
 wrapper.unmount()
})
it('failed reads can be retried and steps still navigate to their configuration', async () => {
 vi.mocked(apiGet).mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(state())
 const { wrapper, router } = await render()
 expect(wrapper.text()).toContain('暂时无法读取部署状态')
 await wrapper.findAll('button').find(b => b.text() === '刷新')!.trigger('click'); await flushPromises()
 expect(wrapper.text()).not.toContain('暂时无法读取部署状态')
 await wrapper.findAll('button').find(b => b.text() === '去检测 / 安装')!.trigger('click'); await flushPromises()
 expect(router.currentRoute.value.path).toBe('/admin/servers')
 wrapper.unmount()
})
it('unmount aborts an unfinished read', async () => {
 let signal: AbortSignal|undefined
 vi.mocked(apiGet).mockImplementation((_path, options) => { signal = options?.signal as AbortSignal; return new Promise(() => {}) })
 const { wrapper } = await render()
 expect(wrapper.text()).toContain('部署检查清单')
 wrapper.unmount(); expect(signal?.aborted).toBe(true)
})
