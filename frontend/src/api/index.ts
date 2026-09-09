/* ---------- API 封装 ---------- */

import { useAuthStore } from '@/stores/auth'
import { withRequestDeadline, type ApiRequestOptions } from './network'
export type { ApiRequestOptions } from './network'

export interface ApiError extends Error {
  status: number
}

async function request<T = any>(path: string, opts: RequestInit & ApiRequestOptions = {}, raw = false): Promise<T> {
  return withRequestDeadline(opts, async signal => {
    const { timeoutMs: _timeoutMs, ...init } = opts
    const auth = useAuthStore()
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...(opts.headers as Record<string, string> || {}),
    }
    if (auth.token) {
      headers['Authorization'] = 'Bearer ' + auth.token
    }

    const res = await fetch(path, {
      ...init,
      signal,
      headers,
      credentials: 'include',
    })

    let body: any = null
    try { body = await res.json() } catch (err) {
      if (signal.aborted) throw signal.reason
      if (res.ok) throw err
    }

    if (!res.ok) {
      if (res.status === 401) {
        auth.logout(true)
        // Session died mid-use — bounce to the public page with the login prompt so
        // the user isn't stranded on a dead screen of failing calls. Skip when already
        // on the public page (its own authless calls must not cause a redirect loop).
        const h = window.location.hash
        if (path !== '/api/auth/me' && !h.startsWith('#/oauth2/callback') && h && h !== '#/' && !h.startsWith('#/?')) {
          window.location.hash = '/?login=1'
        }
      }
      const err = new Error((body && body.msg) || `请求失败 ${res.status}`) as ApiError
      err.status = res.status
      throw err
    }
    // Most endpoints reply with the {code,data,msg} envelope, so we unwrap .data.
    // A few (e.g. sing-box config preview) write a raw JSON document straight to
    // the body — those have no .data, so unwrapping would yield null. `raw` returns
    // the whole parsed body for those callers.
    return raw ? (body as T) : (body?.data ?? null)
  })
}

/** GET，返回列表时保证是数组 */
export async function apiList<T = any>(path: string, options: ApiRequestOptions = {}): Promise<T[]> {
  const data = await request<T[]>(path, options)
  return Array.isArray(data) ? data : []
}

/** GET，返回单个对象 */
export function apiGet<T = any>(path: string, options: ApiRequestOptions = {}): Promise<T> {
  return request<T>(path, options)
}

/**
 * GET，返回未拆封的原始响应体（用于直接返回 JSON 文档、而非 {code,data,msg}
 * 信封的接口，如 sing-box 配置预览）。
 */
export function apiGetRaw<T = any>(path: string, options: ApiRequestOptions = {}): Promise<T> {
  return request<T>(path, options, true)
}

export function apiPost<T = any>(path: string, body?: any, options: ApiRequestOptions = {}): Promise<T> {
  return request<T>(path, {
    ...options,
    method: 'POST',
    body: body ? JSON.stringify(body) : undefined,
  })
}

export function apiPut<T = any>(path: string, body?: any, options: ApiRequestOptions = {}): Promise<T> {
  return request<T>(path, {
    ...options,
    method: 'PUT',
    body: body ? JSON.stringify(body) : undefined,
  })
}

export function apiDelete<T = any>(path: string, options: ApiRequestOptions = {}): Promise<T> {
  return request<T>(path, { ...options, method: 'DELETE' })
}

/**
 * GET 一个二进制附件并触发浏览器下载。
 *
 * 不能用 <a href> 直接下载：认证走的是 Authorization 头，普通导航带不上，
 * 服务端只会回 401。所以先 fetch 成 blob，再用临时 object URL 触发保存。
 */
export async function apiDownload(path: string, fallbackName: string, options: ApiRequestOptions = {}): Promise<void> {
  return withRequestDeadline(options, async signal => {
    const auth = useAuthStore()
    const res = await fetch(path, {
      signal,
      headers: auth.token ? { Authorization: 'Bearer ' + auth.token } : {},
      credentials: 'include',
    })
    if (!res.ok) {
      // 失败时后端回的是 JSON 信封，读出来当错误信息用。
      let msg = `请求失败 ${res.status}`
      try { const b = await res.json(); if (b?.msg) msg = b.msg } catch {}
      const err = new Error(msg) as ApiError
      err.status = res.status
      throw err
    }
    // 优先用服务端给的文件名（Content-Disposition），它带了生成时间。
    let name = fallbackName
    const cd = res.headers.get('Content-Disposition') || ''
    const m = /filename="?([^"';]+)"?/.exec(cd)
    if (m) name = m[1]

    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    // 立刻撤销会让部分浏览器取消尚未开始的下载，推迟一拍。
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
  })
}
