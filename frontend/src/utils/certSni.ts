// 引用证书时 SNI 的预填规则，按后端 singbox.EffectiveServerName 的思路（#119/#121，前端只看域名、不解析证书，最终以后端为准）：
// 默认取证书域名；通配证书（*.example.com）下，已填写且被它覆盖的具体子域名保留，
// 否则会把 "*.example.com" 本身当成 SNI 下发，客户端无法按它校验。
export function sniForCert(current: string | undefined, certDomain: string): string {
  const domain = (certDomain || '').trim().toLowerCase()
  const cur = (current || '').trim().toLowerCase()
  if (!domain.startsWith('*.') || !cur || cur.startsWith('*.')) return certDomain
  const base = domain.slice(2)
  if (!cur.endsWith('.' + base)) return certDomain
  const label = cur.slice(0, cur.length - base.length - 1)
  // 通配符只匹配一级标签。
  return label && !label.includes('.') ? (current || '').trim() : certDomain
}
