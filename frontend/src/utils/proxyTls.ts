// HTTPS 代理（mixed+TLS）的证书提示。
//
// 代理 URL（https://user:pass@host:port）里没有地方写 SNI 或证书指纹：git、Docker、
// curl 等客户端一律按「连接的地址」校验证书。证书只签给某个域名（多节点共用证书很
// 常见）而用户拿到的是 IP 时，就会报「doesn't contain any IP SANs」(#115)。所以这里
// 把后端给出的证书信息翻译成用户能照做的提示，链接格式本身不变。

export interface HttpsProxyTlsFields {
  tls?: boolean
  host?: string
  sni?: string
  cert_domain?: string
  cert_names?: string[]
  self_signed?: boolean
  sha256?: string
  insecure?: boolean
  host_mismatch?: boolean
}

export interface HttpsProxyHint {
  level: 'warning' | 'info'
  text: string
}

// 把 64 位十六进制指纹按字节加冒号，方便和客户端里显示的指纹逐段比对。
export function formatFingerprint(hex?: string): string {
  if (!hex) return ''
  const clean = hex.replace(/[^0-9a-fA-F]/g, '').toUpperCase()
  return clean.match(/.{1,2}/g)?.join(':') ?? clean
}

export function httpsProxyHints(p: HttpsProxyTlsFields): HttpsProxyHint[] {
  if (!p || !p.tls) return []
  const hints: HttpsProxyHint[] = []
  const names = (p.cert_names && p.cert_names.length ? p.cert_names : (p.cert_domain ? [p.cert_domain] : [])).join('、')
  if (p.host_mismatch) {
    hints.push({
      level: 'warning',
      text: `证书签发给 ${names || '其他名称'}，不包含地址 ${p.host}。按地址连接的工具（git、Docker、curl 等）会报「doesn't contain any IP SANs」或主机名不匹配。`
        + (p.sni ? `客户端如支持自定义 TLS 服务器名，请填 ${p.sni}；` : '')
        + '否则只能跳过证书校验（如 curl 的 --proxy-insecure），或请管理员换成覆盖该地址的证书。',
    })
  }
  if (p.self_signed) {
    hints.push({
      level: p.host_mismatch ? 'info' : 'warning',
      text: '这是自签证书，系统默认不信任：需要把证书导入客户端信任列表、按下方指纹固定，或跳过证书校验（curl 用 --proxy-insecure）。',
    })
  }
  return hints
}
