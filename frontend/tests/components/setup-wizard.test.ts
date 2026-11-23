import { expect, it } from 'vitest'
import {
  SCENARIOS, scenarioByKey, visibleSteps, defaultUseRealCert, certCoversDomain, matchingCerts,
  scenarioForInbound, inboundOptions, joinDomain, splitDomain, canChooseCertMode, verifyVerdict,
} from '@/utils/setupWizard'

it('HTTPS proxy walks the full domain → cert → TLS path', () => {
  const s = scenarioByKey('https-proxy')
  expect(defaultUseRealCert(s)).toBe(true)
  expect(visibleSteps(s, true)).toEqual(['goal', 'server', 'domain', 'cert', 'tls', 'inbound', 'verify'])
})

it('Hysteria2 / TUIC default to a quick self-signed cert and skip the domain steps', () => {
  for (const key of ['hysteria2', 'tuic']) {
    const s = scenarioByKey(key)
    expect(defaultUseRealCert(s)).toBe(false)
    expect(visibleSteps(s, false)).toEqual(['goal', 'server', 'selfsigned', 'inbound', 'verify'])
    // …but the user can still opt in to a real domain certificate.
    expect(canChooseCertMode(s)).toBe(true)
    expect(visibleSteps(s, true)).toContain('domain')
  }
})

it('Reality and Shadowsocks never ask for a domain or certificate', () => {
  expect(visibleSteps(scenarioByKey('vless-reality'), true)).toEqual(['goal', 'server', 'reality', 'inbound', 'verify'])
  expect(visibleSteps(scenarioByKey('shadowsocks'), true)).toEqual(['goal', 'server', 'inbound', 'verify'])
  expect(canChooseCertMode(scenarioByKey('vless-reality'))).toBe(false)
  expect(canChooseCertMode(scenarioByKey('shadowsocks'))).toBe(false)
})

it('every scenario resolves to a unique key and a supported inbound type', () => {
  const keys = SCENARIOS.map(s => s.key)
  expect(new Set(keys).size).toBe(keys.length)
  for (const s of SCENARIOS) expect(['mixed', 'trojan', 'vless', 'hysteria2', 'tuic', 'shadowsocks']).toContain(s.type)
})

it('certificate matching: exact, single-label wildcard, never a bare IP or sibling domain', () => {
  expect(certCoversDomain('vps-taxi.mllt.vip', 'VPS-Taxi.mllt.vip')).toBe(true)
  expect(certCoversDomain('*.mllt.vip', 'vps-taxi.mllt.vip')).toBe(true)
  expect(certCoversDomain('*.mllt.vip', 'a.b.mllt.vip')).toBe(false)
  expect(certCoversDomain('*.mllt.vip', 'mllt.vip')).toBe(false)
  expect(certCoversDomain('vps-taxi.mllt.vip', '217.216.188.164')).toBe(false)
  expect(certCoversDomain('', 'a.b')).toBe(false)
  const certs = [
    { id: 1, domain: 'vps-taxi.mllt.vip', status: 'valid' },
    { id: 2, domain: '*.mllt.vip', status: 'expired' },
    { id: 3, domain: '*.mllt.vip', status: 'expiring' },
    { id: 4, domain: 'other.example', status: 'valid' },
  ]
  expect(matchingCerts(certs, 'vps-taxi.mllt.vip').map(c => c.id)).toEqual([1, 3])
})

it('editing maps an existing inbound back to a scenario, Reality by its TLS profile', () => {
  expect(scenarioForInbound({ type: 'mixed' })!.key).toBe('https-proxy')
  expect(scenarioForInbound({ type: 'vless' }, { server_json: '{"reality":{"enabled":true}}' })!.key).toBe('vless-reality')
  expect(scenarioForInbound({ type: 'vless' }, { server_json: '{"enabled":true}' })!.key).toBe('vless-ws-tls')
  expect(scenarioForInbound({ type: 'vmess' })).toBeNull()
})

it('edit keeps the user\'s existing options; create uses the scenario defaults', () => {
  const s = scenarioByKey('hysteria2')
  const existing = '{"up_mbps":300,"obfs":{"type":"salamander","password":"x"}}'
  expect(inboundOptions(s, existing)).toBe(existing)
  expect(JSON.parse(inboundOptions(s))).toEqual({ up_mbps: 100, down_mbps: 100 })
  expect(JSON.parse(inboundOptions(s, 'not json'))).toEqual({ up_mbps: 100, down_mbps: 100 })
})

it('domain helpers round-trip prefix and zone, preferring the longest zone', () => {
  expect(joinDomain(' VPS-Taxi. ', '.mllt.vip')).toBe('vps-taxi.mllt.vip')
  expect(joinDomain('', 'mllt.vip')).toBe('')
  expect(splitDomain('vps-taxi.mllt.vip', ['vip', 'mllt.vip'])).toEqual({ prefix: 'vps-taxi', zone: 'mllt.vip' })
  expect(splitDomain('x.other.com', ['mllt.vip'])).toEqual({ prefix: '', zone: '' })
})

it('a TLS warning is a reminder, not a failure', () => {
  expect(verifyVerdict('warning').type).toBe('warning')
  expect(verifyVerdict('warning').text).toContain('不影响使用')
  expect(verifyVerdict('error').type).toBe('error')
  expect(verifyVerdict('skipped').type).toBe('default')
})
