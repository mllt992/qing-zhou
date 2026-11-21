import { expect, it } from 'vitest'
import { formatFingerprint, httpsProxyHints } from '@/utils/proxyTls'

it('flags a shared domain certificate served on an IP (#115)', () => {
  const hints = httpsProxyHints({ tls: true, host: '217.216.188.164', sni: 'ssl.mllt.vip', cert_names: ['ssl.mllt.vip'], self_signed: true, host_mismatch: true })
  expect(hints).toHaveLength(2)
  expect(hints[0].level).toBe('warning')
  expect(hints[0].text).toContain('217.216.188.164')
  expect(hints[0].text).toContain('ssl.mllt.vip')
  expect(hints[0].text).toContain('--proxy-insecure')
  expect(hints[1].text).toContain('自签')
})

it('says nothing for plain proxies or a matching CA certificate', () => {
  expect(httpsProxyHints({ tls: false, host: '1.2.3.4' })).toEqual([])
  expect(httpsProxyHints({ tls: true, host: 'a.example', cert_names: ['a.example'] })).toEqual([])
})

it('self-signed but matching only warns about trust', () => {
  const hints = httpsProxyHints({ tls: true, host: '1.2.3.4', cert_names: ['1.2.3.4'], self_signed: true })
  expect(hints).toHaveLength(1)
  expect(hints[0].level).toBe('warning')
})

it('formats fingerprints byte by byte', () => {
  expect(formatFingerprint('abcd12')).toBe('AB:CD:12')
  expect(formatFingerprint(undefined)).toBe('')
})
