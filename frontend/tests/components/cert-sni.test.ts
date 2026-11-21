import { expect, it } from 'vitest'
import { sniForCert } from '@/utils/certSni'

// #121：前端选证书时的 SNI 规则必须与后端（sing-box 配置与订阅链接共用）一致。
it('uses the certificate domain by default', () => {
  expect(sniForCert('www.microsoft.com', 'ssl.mllt.vip')).toBe('ssl.mllt.vip')
  expect(sniForCert('', 'ssl.mllt.vip')).toBe('ssl.mllt.vip')
})

it('keeps a concrete name covered by a wildcard certificate', () => {
  expect(sniForCert('node1.mllt.vip', '*.mllt.vip')).toBe('node1.mllt.vip')
  expect(sniForCert(' Node1.mllt.vip ', '*.mllt.vip')).toBe('Node1.mllt.vip')
})

it('falls back to the wildcard domain when the name is not covered', () => {
  expect(sniForCert('a.b.mllt.vip', '*.mllt.vip')).toBe('*.mllt.vip') // 通配只覆盖一级
  expect(sniForCert('mllt.vip', '*.mllt.vip')).toBe('*.mllt.vip')
  expect(sniForCert('other.example', '*.mllt.vip')).toBe('*.mllt.vip')
  expect(sniForCert('*.mllt.vip', '*.mllt.vip')).toBe('*.mllt.vip')
})
