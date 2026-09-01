import { useConfigStore } from '@/stores/config'
export function fmtBytes(n: number | null | undefined): string {
  if (n == null || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  n = Math.abs(n)
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return (n < 10 && i > 0 ? n.toFixed(2) : n < 100 && i > 0 ? n.toFixed(1) : Math.round(n).toString()) + ' ' + units[i]
}

export function fmtTotal(n: number | null | undefined): string {
  return fmtBytes(n)
}

export function fmtDate(ts: number | null | undefined): string {
  if (!ts) return '永久'
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

export function fmtDateTime(ts: number | null | undefined): string {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// toLocalDatetimeInput formats a unix timestamp for an <input type="datetime-local">,
// which reads/writes LOCAL wall-clock. Using toISOString() here (as the old code did)
// emits UTC, so any admin outside UTC saw — and, on save, shifted — the time by their
// offset on every edit round-trip. Build the local components explicitly instead.
export function toLocalDatetimeInput(ts: number | null | undefined): string {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

export function daysLeft(ts: number | null | undefined): number | null {
  if (!ts) return null
  return Math.ceil((ts * 1000 - Date.now()) / 86400000)
}


// Converts a points balance to its CNY equivalent. The exchange rate comes
// from site config (points_per_cny, admin-editable); the old hardcoded default
// of 10 ignored that setting, so the shop/dashboard showed wrong prices
// whenever the admin changed the rate.
export function yuan(points: number | null | undefined, rate?: number): string {
  const r = rate ?? useConfigStore().config.points_per_cny ?? 10
  if (!points) return '≈¥0'
  return '≈¥' + (points / r).toFixed(points % r ? 1 : 0)
}

export function pct(used: number | null | undefined, total: number | null | undefined): number {
  if (!total || total <= 0) return 0
  return Math.min(100, Math.round(((used || 0) / total) * 1000) / 10)
}

export function fmtUptime(seconds: number | null | undefined): string {
  if (!seconds || seconds <= 0) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}天${h}时`
  if (h > 0) return `${h}时${m}分`
  return `${m}分`
}

export function timeAgo(ts: number | null | undefined): string {
  if (!ts) return '—'
  const diff = Math.floor(Date.now() / 1000) - ts
  if (diff < 60) return '刚刚'
  if (diff < 3600) return Math.floor(diff / 60) + '分钟前'
  if (diff < 86400) return Math.floor(diff / 3600) + '小时前'
  return Math.floor(diff / 86400) + '天前'
}

function p(n: number): string {
  return String(n).padStart(2, '0')
}
