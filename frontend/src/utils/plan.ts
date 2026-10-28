// Shared rendering helpers for a user's plan buckets (planView from the API),
// so the user dashboard and the admin user detail present status/timing the same
// way. `status` comes from the backend (active | queued | exhausted | expired);
// `activate_by` is a queued plan's estimated latest activation time (unix, 0 =
// unknown).

export type PlanTagType = 'success' | 'info' | 'warning' | 'error' | 'default'

/** Status label + Naive-UI tag type for a plan bucket. */
export function planStatusMeta(p: any): { label: string; type: PlanTagType } {
  if (p.status === 'queued') return { label: '排队中', type: 'info' }
  if (p.status === 'expired' || (p.status !== 'queued' && p.expiry_at && p.expiry_at * 1000 < Date.now())) {
    return { label: '已过期', type: 'error' }
  }
  // 按月重置的套餐用完只是暂停到下次重置；预支时用量会超过本月额度，仍算使用中
  if (p.periodic) {
    if (p.status === 'exhausted') return { label: '本月已用完', type: 'warning' }
    return { label: '使用中', type: 'success' }
  }
  if (p.status === 'exhausted' || (p.traffic_limit > 0 && p.used >= p.traffic_limit)) {
    return { label: '已用尽', type: 'warning' }
  }
  return { label: '使用中', type: 'success' }
}

/** The date line for a plan row: expiry for active plans, estimated activation
 *  for queued ones. fmtDate formats a unix seconds value. */
export function planTimeText(p: any, fmtDate: (t: number) => string): string {
  if (p.status === 'queued') {
    return p.activate_by ? `预计 ${fmtDate(p.activate_by)} 前生效` : '前一份用完后生效'
  }
  return p.expiry_at ? fmtDate(p.expiry_at) : '不过期'
}

/** Usage line for one plan row. Monthly-reset plans show this period only. */
export function planUsageText(p: any, fmtBytes: (n: number) => string, fmtTotal: (n: number) => string, fmtDate: (t: number) => string): string {
  if (p.status === 'queued') return p.periodic ? '每月流量 ' + fmtTotal(p.period_quota || p.traffic_limit) : '待用流量 ' + fmtTotal(p.traffic_limit)
  const head = p.periodic ? '本月已用 ' : '已用 '
  let s = p.traffic_limit <= 0
    ? `${head}${fmtBytes(p.used)} / 0 B · 剩 0 B`
    : `${head}${fmtBytes(p.used)} / ${fmtTotal(p.traffic_limit)} · 剩 ${fmtBytes(p.remaining < 0 ? 0 : p.remaining)}`
  if (p.periodic && p.advanced > 0) s += ` · 已预支下期 ${fmtBytes(p.advanced)}`
  if (p.periodic && p.next_reset_at && p.status !== 'expired') s += ` · ${fmtDate(p.next_reset_at)} 重置`
  return s
}

/** Sort key so a list reads active first, then queued, then finished. */
export function planSortKey(p: any): number {
  const m = planStatusMeta(p)
  if (m.label === '使用中' || m.label === '本月已用完') return 0
  if (m.label === '排队中') return 1
  return 2
}
