// 配置向导（/admin/setup）的纯逻辑：场景、步骤编排、证书匹配。
// 不依赖 Vue / 网络，便于单测；页面只负责把这些规则接到现有接口上。

export type CertNeed = 'required' | 'recommended' | 'optional' | 'none'
export type TlsKind = 'cert' | 'reality' | 'none'
export type StepKey = 'goal' | 'server' | 'domain' | 'cert' | 'tls' | 'selfsigned' | 'reality' | 'inbound' | 'verify'

export interface Scenario {
  key: string
  title: string
  desc: string
  /** 证书对该协议的重要程度：required=客户端会校验；optional=客户端通常不校验 */
  cert: CertNeed
  tls: TlsKind
  type: string
  tag: string
  port: number
  /** 新建时写入入站 options 的协议默认值 */
  options: Record<string, any>
}

export const SCENARIOS: Scenario[] = [
  { key: 'https-proxy', title: 'HTTPS 代理', cert: 'required', tls: 'cert', type: 'mixed', tag: 'https-proxy', port: 7648, options: {},
    desc: '给 1Panel / Docker / curl / Git 当代理用。客户端会校验证书，推荐用域名 + 真实证书。' },
  { key: 'trojan', title: 'Trojan + TLS', cert: 'recommended', tls: 'cert', type: 'trojan', tag: 'trojan', port: 443, options: {},
    desc: '伪装成普通 HTTPS 流量。建议用域名 + 真实证书；客户端也可配置跳过校验。' },
  { key: 'vless-ws-tls', title: 'VLESS + WS + TLS', cert: 'recommended', tls: 'cert', type: 'vless', tag: 'vless-ws', port: 443,
    options: { flow: 'none', transport: { type: 'ws', path: '/ws' } },
    desc: '可套 CDN。建议用域名 + 真实证书。' },
  { key: 'hysteria2', title: 'Hysteria2', cert: 'optional', tls: 'cert', type: 'hysteria2', tag: 'hy2', port: 8443,
    options: { up_mbps: 100, down_mbps: 100 },
    desc: '基于 UDP/QUIC，速度快。客户端通常不校验证书，默认一键自签即可，也可改用域名证书。' },
  { key: 'tuic', title: 'TUIC', cert: 'optional', tls: 'cert', type: 'tuic', tag: 'tuic', port: 8443,
    options: { congestion_control: 'bbr' },
    desc: '基于 UDP/QUIC。客户端通常不校验证书，默认一键自签即可，也可改用域名证书。' },
  { key: 'vless-reality', title: 'VLESS + Reality', cert: 'none', tls: 'reality', type: 'vless', tag: 'vless-reality', port: 443,
    options: { flow: 'xtls-rprx-vision' },
    desc: '不需要域名和证书，借用别人网站的 TLS 握手，抗封锁能力强。' },
  { key: 'shadowsocks', title: 'Shadowsocks', cert: 'none', tls: 'none', type: 'shadowsocks', tag: 'ss', port: 8388,
    options: { method: '2022-blake3-aes-128-gcm' },
    desc: '最简单，不需要域名、证书和 TLS。' },
]

export const QUIC_TYPES = ['tuic', 'hysteria', 'hysteria2']

export function scenarioByKey(key: string): Scenario {
  return SCENARIOS.find(s => s.key === key) || SCENARIOS[0]
}

/** 该场景默认是否使用"域名 + 真实证书"路径。 */
export function defaultUseRealCert(s: Scenario): boolean {
  return s.cert === 'required' || s.cert === 'recommended'
}

/** 场景允许在"域名证书"和"一键自签"之间选择（Reality / SS 没有这个选择）。 */
export function canChooseCertMode(s: Scenario): boolean {
  return s.tls === 'cert'
}

/** 按场景和证书路径决定要走哪些步骤（顺序固定）。 */
export function visibleSteps(s: Scenario, useRealCert: boolean): StepKey[] {
  const head: StepKey[] = ['goal', 'server']
  const tail: StepKey[] = ['inbound', 'verify']
  if (s.tls === 'none') return [...head, ...tail]
  if (s.tls === 'reality') return [...head, 'reality', ...tail]
  return useRealCert ? [...head, 'domain', 'cert', 'tls', ...tail] : [...head, 'selfsigned', ...tail]
}

export const STEP_TITLES: Record<StepKey, string> = {
  goal: '选择用途', server: '选择服务器', domain: '域名与解析', cert: '证书', tls: 'TLS 配置',
  selfsigned: '自签证书', reality: 'Reality 参数', inbound: '创建入站', verify: '验证',
}

/** 单级通配符：*.example.com 只覆盖 a.example.com，不覆盖 a.b.example.com。 */
export function certCoversDomain(certDomain: string, domain: string): boolean {
  const c = (certDomain || '').trim().toLowerCase()
  const d = (domain || '').trim().toLowerCase()
  if (!c || !d) return false
  if (c === d) return true
  if (!c.startsWith('*.')) return false
  const base = c.slice(1) // ".example.com"
  if (!d.endsWith(base)) return false
  const label = d.slice(0, d.length - base.length)
  return label.length > 0 && !label.includes('.')
}

/** 能直接用的证书：覆盖该域名且未过期 / 可解密。 */
export function matchingCerts(certs: any[], domain: string): any[] {
  return (certs || []).filter(c =>
    c && c.status !== 'expired' && c.status !== 'decrypt_failed' && certCoversDomain(c.domain, domain))
}

/** 解析 TLS 配置里的 server_name（SNI）。 */
export function tlsServerName(tls?: { server_json?: string } | null): string {
  if (!tls || !tls.server_json) return ''
  try {
    const o = typeof tls.server_json === 'string' ? JSON.parse(tls.server_json) : tls.server_json
    return typeof o?.server_name === 'string' ? o.server_name.trim() : ''
  } catch { return '' }
}

/**
 * 查找可复用的证书型 TLS 配置：必须引用同一张证书、非 Reality、
 * 所属机器为全局(0)或当前服务器，且 SNI 与当前域名一致（防止泛域名证书跨子域名误复用不同 SNI）。
 */
export function matchingCertTls(tlsList: any[], certId: number, domain: string, serverId?: number | null): any | null {
  const d = (domain || '').trim().toLowerCase()
  if (!certId || !d) return null
  const sid = serverId || 0
  return (tlsList || []).find(t =>
    t && t.cert_id === certId && t.mode !== 'reality' && !(t.server_json || '').includes('"reality"')
    && (!t.server_id || t.server_id === sid)
    && tlsServerName(t).toLowerCase() === d) || null
}

/** 从已有入站反推向导场景；向导不支持的协议返回 null（让用户去 sing-box 页面改）。 */
export function scenarioForInbound(inbound: { type: string }, tls?: { server_json?: string } | null): Scenario | null {
  switch (inbound.type) {
    case 'mixed': return scenarioByKey('https-proxy')
    case 'trojan': return scenarioByKey('trojan')
    case 'hysteria2': return scenarioByKey('hysteria2')
    case 'tuic': return scenarioByKey('tuic')
    case 'shadowsocks': return scenarioByKey('shadowsocks')
    case 'vless':
      return scenarioByKey(tls && (tls.server_json || '').includes('"reality"') ? 'vless-reality' : 'vless-ws-tls')
    default: return null
  }
}

/**
 * 入站 options：新建用场景默认值；编辑时原样保留已有 options，
 * 向导只改 tag / 端口 / TLS / 服务器，不动用户在高级页里调过的参数。
 */
export function inboundOptions(s: Scenario, existing?: string): string {
  if (existing && existing.trim()) {
    try { JSON.parse(existing); return existing } catch { /* fall through to defaults */ }
  }
  return JSON.stringify(s.options)
}

/** 由前缀 + 根域名拼出完整域名；任一为空返回空串。 */
export function joinDomain(prefix: string, zone: string): string {
  const p = (prefix || '').trim().replace(/\.+$/, '').toLowerCase()
  const z = (zone || '').trim().replace(/^\.+/, '').toLowerCase()
  return p && z ? `${p}.${z}` : ''
}

/** 从完整域名里按已有 Zone 反推前缀（Zone 取最长匹配）。 */
export function splitDomain(domain: string, zones: string[]): { prefix: string; zone: string } {
  const d = (domain || '').trim().toLowerCase()
  const zone = zones.filter(z => d.endsWith('.' + z.toLowerCase())).sort((a, b) => b.length - a.length)[0] || ''
  return zone ? { prefix: d.slice(0, d.length - zone.length - 1), zone } : { prefix: '', zone: '' }
}

/** TLS 检测结果里是否有需要展示的提醒（warning 只是提醒，不阻止完成）。 */
export function verifyVerdict(status: string): { type: 'success' | 'warning' | 'error' | 'default'; text: string } {
  switch (status) {
    case 'ok': return { type: 'success', text: '全部正常' }
    case 'warning': return { type: 'warning', text: '有提醒（不影响使用）' }
    case 'error': return { type: 'error', text: '存在问题，需要处理' }
    default: return { type: 'default', text: '无需检测' }
  }
}
