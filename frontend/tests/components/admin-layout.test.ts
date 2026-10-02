import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, matchedRouteKey } from 'vue-router'
import { computed } from 'vue'
import { NButton, NSwitch } from 'naive-ui'
import Monitor from '@/views/Monitor.vue'
import UserDashboard from '@/views/UserDashboard.vue'
import AdminSettings from '@/views/AdminSettings.vue'
import AdminOnboarding from '@/components/AdminOnboarding.vue'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import { apiGet, apiList, apiPut } from '@/api'

vi.mock('@/api', () => ({ apiGet: vi.fn(), apiList: vi.fn(), apiPut: vi.fn(), apiPost: vi.fn(), apiDelete: vi.fn(), apiDownload: vi.fn() }))
vi.mock('naive-ui', async importOriginal => ({
  ...await importOriginal<typeof import('naive-ui')>(),
  useMessage: () => ({ error: vi.fn(), success: vi.fn(), warning: vi.fn() }),
  useDialog: () => ({ warning: vi.fn() }),
}))
vi.mock('echarts', () => ({ init: () => ({ setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn(), clear: vi.fn(), on: vi.fn() }) }))

const administrator = { id: 1, username: 'admin', email: '', email_verified: true, role: 'admin', is_admin: true, status: 'active', points: 0 }
let settings: Record<string, string>
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(window.matchMedia).mockImplementation(query => ({ matches: false, media: query, onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }))
  settings = { site_name: 'Test', homepage_mode: 'monitor' }
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.mocked(apiList).mockResolvedValue([])
  vi.mocked(apiGet).mockImplementation(async path => {
    if (path === '/api/admin/settings') return { ...settings }
    if (path === '/api/config') return { homepage_machine_health: settings.homepage_machine_health === 'true' }
    if (path === '/api/admin/upstreams') return []
    if (path === '/api/admin/onboarding') return { complete: true, steps: [], checked_at: 1 }
    if (path.startsWith('/api/admin/monitor/health-timeline')) return { machines: [], from: 1, to: 2 }
    return {}
  })
  vi.mocked(apiPut).mockImplementation(async (_path, body) => {
    Object.assign(settings, body)
    return { ...settings }
  })
})
afterEach(() => vi.restoreAllMocks())

async function render(component: any, role = 'admin', path = '/') {
  const pinia = createPinia(); setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = role === 'guest' ? null : { ...administrator, is_admin: role === 'admin', role }
  auth.loaded = true
  const config = useConfigStore()
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
  ] })
  await router.push(path); await router.isReady()
  const wrapper = shallowMount(component, {
    global: {
      plugins: [pinia, router], renderStubDefaultSlot: true,
      provide: { [matchedRouteKey as symbol]: computed(() => router.currentRoute.value.matched[0]) },
      stubs: { MachineHealthStrip: false, AdminOnboarding: false },
    },
  })
  await flushPromises()
  return { wrapper, auth, config, router }
}

const healthReads = () => vi.mocked(apiGet).mock.calls.filter(([path]) => path.includes('/health-timeline'))

describe('machine health homepage opt-in', () => {
  for (const component of [Monitor, UserDashboard]) {
    it(`defaults hidden and only loads after enabling on ${component.__name}`, async () => {
      const { wrapper, auth, config } = await render(component)
      expect(config.config.homepage_machine_health).toBe(false)
      expect(healthReads()).toHaveLength(0)
      config.config.homepage_machine_health = true
      await flushPromises()
      expect(healthReads()).toHaveLength(1)
      expect(wrapper.findComponent({ name: 'MachineHealthStrip' }).exists()).toBe(true)
      config.config.homepage_machine_health = false
      await flushPromises()
      expect(wrapper.findComponent({ name: 'MachineHealthStrip' }).exists()).toBe(false)
      config.config.homepage_machine_health = true
      await flushPromises()
      expect(healthReads()).toHaveLength(2)
      auth.user = null
      await flushPromises()
      expect(wrapper.findComponent({ name: 'MachineHealthStrip' }).exists()).toBe(false)
      wrapper.unmount()
    })
  }
  for (const role of ['guest', 'user']) {
    it(`never loads administrator health for ${role}, even when enabled`, async () => {
      const { wrapper, config } = await render(Monitor, role)
      config.config.homepage_machine_health = true
      await flushPromises()
      expect(healthReads()).toHaveLength(0)
      expect(wrapper.findComponent({ name: 'MachineHealthStrip' }).exists()).toBe(false)
      wrapper.unmount()
    })
  }
  it('does not render machine health over a custom homepage', async () => {
    const { wrapper, config } = await render(Monitor)
    config.config.homepage_mode = 'custom'
    config.config.homepage_url = 'https://example.com'
    config.config.homepage_machine_health = true
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(true)
    expect(healthReads()).toHaveLength(0)
    wrapper.unmount()
  })
})

describe('settings sections', () => {
  it('deep-links to checklist and unmounts it when another section is selected', async () => {
    const { wrapper, router } = await render(AdminSettings, 'admin', '/admin/settings?section=onboarding')
    expect(wrapper.findComponent(AdminOnboarding).exists()).toBe(true)
    const home = wrapper.findAll('button').find(button => button.text().includes('首页设置'))!
    await home.trigger('click'); await flushPromises()
    expect(router.currentRoute.value.query.section).toBe('home')
    expect(wrapper.findComponent(AdminOnboarding).exists()).toBe(false)
    await router.push('/admin/settings?section=onboarding'); await flushPromises()
    expect(wrapper.findComponent(AdminOnboarding).exists()).toBe(true)
    wrapper.unmount()
  })
  it('saves homepage visibility through existing settings and reloads the persisted value', async () => {
    const { wrapper, config } = await render(AdminSettings, 'admin', '/admin/settings?section=home')
    const toggle = wrapper.findAllComponents(NSwitch).find(item => item.attributes('aria-label') === '首页显示机器健康')!
    expect(toggle.props('value')).toBe('false')
    toggle.vm.$emit('update:value', 'true')
    await flushPromises()
    const save = wrapper.findAllComponents(NButton).find(button => button.text() === '保存设置')!
    expect(save.exists()).toBe(true)
    save.vm.$emit('click'); await flushPromises()
    expect(apiPut).toHaveBeenCalledWith('/api/admin/settings', expect.objectContaining({ homepage_machine_health: 'true' }))
    expect(config.config.homepage_machine_health).toBe(true)
    wrapper.unmount()
    const reloaded = await render(AdminSettings, 'admin', '/admin/settings?section=home')
    const savedToggle = reloaded.wrapper.findAllComponents(NSwitch).find(item => item.attributes('aria-label') === '首页显示机器健康')!
    expect(savedToggle.props('value')).toBe('true')
    reloaded.wrapper.unmount()
  })
})


it('redirects saved overview checklist links to settings after checking administrator access', async () => {
  const pinia = createPinia(); setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = { ...administrator }; auth.loaded = true
  const { default: router } = await import('@/router')
  await router.push('/admin?checklist=1&keep=1')
  expect(router.currentRoute.value.path).toBe('/admin/settings')
  expect(router.currentRoute.value.query).toEqual({ section: 'onboarding', keep: '1' })
  auth.user = null
  await router.push('/admin?checklist=1')
  expect(router.currentRoute.value.path).toBe('/')
  expect(router.currentRoute.value.query.login).toBe('1')
})
