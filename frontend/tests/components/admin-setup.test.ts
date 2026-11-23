import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { NMessageProvider, NDialogProvider } from 'naive-ui'
import { defineComponent, h } from 'vue'
import AdminSetup from '@/views/AdminSetup.vue'

const mocks = vi.hoisted(() => ({
  apiList: vi.fn(),
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPut: vi.fn(),
}))

vi.mock('@/api', () => ({
  apiList: mocks.apiList,
  apiGet: mocks.apiGet,
  apiPost: mocks.apiPost,
  apiPut: mocks.apiPut,
}))

async function mountSetup(path = '/admin/setup') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/admin/setup', component: AdminSetup },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const Host = defineComponent({
    render: () => h(NMessageProvider, null, {
      default: () => h(NDialogProvider, null, { default: () => h(AdminSetup) }),
    }),
  })
  const wrapper = mount(Host, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}

afterEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

describe('AdminSetup wizard', () => {
  const sampleServers = [
    { id: 1, name: 'TAXI', host: '217.216.188.164', host_v6: '2402:1920:fc:af::2', domain: 'vps-taxi.mllt.vip', domain_v6: 'vps-taxi-v6.mllt.vip' },
  ]
  const sampleCerts = [
    { id: 10, name: 'vps-taxi.mllt.vip', domain: 'vps-taxi.mllt.vip', status: 'valid', days_left: 89, self_signed: false },
  ]
  const sampleTls = [
    { id: 20, name: 'TAXI协议SSL', mode: 'tls', cert_id: 10, server_json: '{"enabled":true,"server_name":"vps-taxi.mllt.vip"}' },
  ]
  const sampleInbounds = [
    { id: 30, server_id: 1, type: 'mixed', tag: 'taxi-proxy', listen: '::', listen_port: 7648, tls_id: 20, enabled: true, upstream_inbound_id: 9, egress_id: 4, options: '{"tcp_fast_open":true}' },
  ]

  function seedApi() {
    mocks.apiList.mockImplementation(async (url: string) => {
      if (url === '/api/admin/servers') return sampleServers
      if (url === '/api/admin/certs') return sampleCerts
      if (url === '/api/admin/sb/tls') return sampleTls
      if (url === '/api/admin/sb/inbounds') return sampleInbounds
      return []
    })
    mocks.apiGet.mockImplementation(async (url: string) => {
      if (url === '/api/admin/cf/zones') return { configured: true, zones: [{ id: 'z1', name: 'mllt.vip' }] }
      if (url === '/api/admin/setup/preflight') return { cf_configured: true, acme_installed: true, acme_email: 'a@b.co' }
      return null
    })
  }

  it('renders create mode by default and lets user pick Hysteria2 with self-signed default', async () => {
    seedApi()
    const { wrapper } = await mountSetup('/admin/setup')
    expect(wrapper.text()).toContain('配置向导')
    expect(wrapper.text()).toContain('新建配置')
    expect(wrapper.text()).toContain('编辑已有入站')

    const hy2 = wrapper.findAll('button.goal-card').find(b => b.text().includes('Hysteria2'))
    expect(hy2).toBeDefined()
    await hy2!.trigger('click')
    expect(wrapper.text()).toContain('通常不校验证书')
  })

  it('loads existing inbound in edit mode and preserves upstream/egress/options on save', async () => {
    seedApi()
    mocks.apiPut.mockResolvedValue({ id: 30 })
    mocks.apiPost.mockResolvedValue({ status: 'warning', results: [{ addr: 'vps-taxi.mllt.vip:7648', status: 'warning', warnings: ['仅提醒'] }] })

    const { wrapper } = await mountSetup('/admin/setup?inbound=30')
    expect(wrapper.text()).toContain('正在编辑入站「taxi-proxy」')

    // Jump straight to the inbound step via step header and save
    const inboundStep = wrapper.findAll('.n-step').find(s => s.text().includes('创建入站'))
    expect(inboundStep).toBeDefined()
    await inboundStep!.trigger('click')
    await flushPromises()

    const saveBtn = wrapper.findAll('button').find(b => b.text().includes('保存并验证'))
    expect(saveBtn).toBeDefined()
    await saveBtn!.trigger('click')
    await flushPromises()

    expect(mocks.apiPut).toHaveBeenCalledWith('/api/admin/sb/inbounds/30', expect.objectContaining({
      type: 'mixed',
      tag: 'taxi-proxy',
      listen_port: 7648,
      tls_id: 20,
      server_id: 1,
      upstream_inbound_id: 9,
      egress_id: 4,
      options: '{"tcp_fast_open":true}',
    }))
  })

  it('does not reuse a wildcard cert TLS profile whose SNI belongs to another subdomain', async () => {
    seedApi()
    mocks.apiList.mockImplementation(async (url: string) => {
      if (url === '/api/admin/servers') return sampleServers
      if (url === '/api/admin/certs') return [{ id: 10, name: 'wildcard', domain: '*.mllt.vip', status: 'valid', days_left: 80, self_signed: false }]
      if (url === '/api/admin/sb/tls') return [{ id: 21, name: 'other-node', mode: 'tls', cert_id: 10, server_id: 0, server_json: '{"enabled":true,"server_name":"other.mllt.vip"}' }]
      if (url === '/api/admin/sb/inbounds') return []
      return []
    })
    mocks.apiPost.mockResolvedValue({ id: 22, name: 'vps-taxi.mllt.vip' })

    const { wrapper } = await mountSetup('/admin/setup?server=1')
    // Jump from domain → cert → tls
    const nextBtn = () => wrapper.findAll('button').find(b => b.text() === '下一步')!
    await nextBtn().trigger('click') // to cert (auto-selects wildcard cert 10)
    await flushPromises()
    await nextBtn().trigger('click') // to tls (triggers ensureTls)
    await flushPromises()

    expect(mocks.apiPost).toHaveBeenCalledWith('/api/admin/sb/tls/cert', expect.objectContaining({
      name: 'vps-taxi.mllt.vip',
      server_name: 'vps-taxi.mllt.vip',
      cert_id: 10,
    }))
  })

  it('invalidates existing TLS profile when the server domain is changed in step 3', async () => {
    seedApi()
    let currentDomain = 'vps-taxi.mllt.vip'
    mocks.apiList.mockImplementation(async (url: string) => {
      if (url === '/api/admin/servers') return [{ ...sampleServers[0], domain: currentDomain }]
      if (url === '/api/admin/certs') return [{ id: 10, name: 'wildcard', domain: '*.mllt.vip', status: 'valid', days_left: 80, self_signed: false }]
      if (url === '/api/admin/sb/tls') return sampleTls
      if (url === '/api/admin/sb/inbounds') return sampleInbounds
      return []
    })
    mocks.apiPost.mockImplementation(async (url: string) => {
      if (url.endsWith('/dns')) { currentDomain = 'taxi2.mllt.vip'; return {} }
      if (url.endsWith('/dns-check')) return { domain: currentDomain, status: 'ok', resolved: ['217.216.188.164'] }
      if (url === '/api/admin/sb/tls/cert') return { id: 99, name: 'taxi2.mllt.vip' }
      return {}
    })

    const { wrapper } = await mountSetup('/admin/setup?inbound=30')
    // Go to domain step and save a new domain under the same wildcard cert
    const domainStep = wrapper.findAll('.n-step').find(s => s.text().includes('域名与解析'))!
    await domainStep.trigger('click')
    await flushPromises()
    const saveManualBtn = wrapper.findAll('button').find(b => b.text().includes('仅保存域名'))!
    await saveManualBtn.trigger('click')
    await flushPromises()

    // Moving to TLS step must create a new TLS profile for taxi2.mllt.vip instead of keeping old tls_id=20
    const tlsStep = wrapper.findAll('.n-step').find(s => s.text().includes('TLS 配置'))!
    await tlsStep.trigger('click')
    await flushPromises()
    expect(mocks.apiPost).toHaveBeenCalledWith('/api/admin/sb/tls/cert', expect.objectContaining({
      server_name: 'taxi2.mllt.vip',
      cert_id: 10,
    }))
  })
})
