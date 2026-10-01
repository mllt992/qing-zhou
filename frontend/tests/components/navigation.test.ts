import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import axe from 'axe-core'
import DashboardLayout from '@/components/DashboardLayout.vue'
import { useAuthStore } from '@/stores/auth'

async function renderNavigation(mobile = false) {
  vi.mocked(window.matchMedia).mockImplementation(query => ({
    matches: mobile && query.includes('768px'), media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(),
    removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }))
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = { id: 1, username: 'admin', email: '', email_verified: true,
    role: 'admin', is_admin: true, status: 'active', points: 0 }
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/:pathMatch(.*)*', component: { template: '<h1>管理页面</h1>' } },
  ] })
  await router.push('/admin'); await router.isReady()
  const wrapper = mount(DashboardLayout, { attachTo: document.body, global: { plugins: [pinia, router] } })
  await flushPromises()
  return { wrapper, router }
}

afterEach(() => { document.body.innerHTML = ''; vi.restoreAllMocks() })
describe('admin navigation accessibility', () => {
  for (const mobile of [false, true]) {
    it(`passes axe on ${mobile ? 'mobile' : 'desktop'} and keeps account button named`, async () => {
      const { wrapper } = await renderNavigation(mobile)
      const result = await axe.run(wrapper.element, {
        runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa'] },
        // jsdom has no layout/color computation; contrast needs browser QA.
        rules: { 'color-contrast': { enabled: false } },
      })
      expect(result.violations.map(v => ({ id: v.id, nodes: v.nodes.map(n => n.target) }))).toEqual([])
      expect(wrapper.get('.account-button').attributes('aria-label')).toBe('admin账户菜单')
      wrapper.unmount()
    })
  }
  it('home link is keyboard focusable and navigation keeps the SPA route', async () => {
    const { wrapper, router } = await renderNavigation()
    const home = wrapper.get('a.sidebar-brand')
    expect(home.attributes('href')).toBe('/')
    ;(home.element as HTMLElement).focus()
    expect(document.activeElement).toBe(home.element)
    await home.trigger('click'); await flushPromises()
    expect(router.currentRoute.value.path).toBe('/')
    wrapper.unmount()
  })
})
