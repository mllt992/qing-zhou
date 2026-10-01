/* ---------- API 封装 ---------- */

import { useAuthStore } from '@/stores/auth'
import { withRequestDeadline, type ApiRequestOptions } from './network'
export type { ApiRequestOptions } from './network'

export interface ApiError extends Error {
  status: number
}

import {
  cachedStepUpProof, cancelStepUpRequests, forgetStepUpProof, obtainStepUpProof, stepUpGeneration,
} from './reauth'

// Remember the server-selected scope per endpoint. The server remains the only
// authority; this map just lets a still-valid proof avoid a redundant challenge.
const requestScopes = new Map<string, string>()
function sessionKey() {
  const auth = useAuthStore()
  return `${auth.user?.id ?? ''}:${auth.token}`
}
function responseError(path: string, status: number, body: any): ApiError {
  const auth = useAuthStore()
  if (status === 401) {
    auth.logout(true)
    const h = window.location.hash
    if (path !== '/api/auth/me' && !h.startsWith('#/oauth2/callback') && h && h !== '#/' && !h.startsWith('#/?')) {
      window.location.hash = '/?login=1'
    }
  }
  const err = new Error(body?.msg || `请求失败 ${status}`) as ApiError
  err.status = status
  return err
}

async function authorizedResponse(path: string, opts: RequestInit & ApiRequestOptions, binary = false) {
  const session = sessionKey()
  const generation = stepUpGeneration
  const key = `${opts.method || 'GET'} ${path}`
  const knownScope = requestScopes.get(key)
  let proof = knownScope ? cachedStepUpProof(knownScope, session) : undefined
  // Retry exactly once, and only a server challenge known to occur before the
  // handler runs. Never replay network failures or ordinary validation errors.
  for (let attempt = 0; attempt < 2; attempt++) {
    const result = await withRequestDeadline(opts, async signal => {
      const { timeoutMs: _timeoutMs, ...init } = opts
      const headers = new Headers(opts.headers)
      headers.set('Content-Type', 'application/json')
      const auth = useAuthStore()
      if (auth.token) headers.set('Authorization', 'Bearer ' + auth.token)
      headers.delete('X-QZ-Step-Up')
      if (proof) headers.set('X-QZ-Step-Up', proof)
      const res = await fetch(path, { ...init, signal, headers, credentials: 'include' })
      let body: any = null
      if (binary && res.ok) body = await res.blob()
      else {
        try { body = await res.json() } catch (err) {
          if (signal.aborted) throw signal.reason
          if (res.ok) throw err
        }
      }
      return { res, body }
    })
    const { res, body } = result
    if (res.ok) {
      if (path === '/api/user/password') cancelStepUpRequests(true)
      return result
    }
    if (attempt === 0 && path !== '/api/user/reauth' && res.status === 403 && body?.data?.error === 'step_up_required' && typeof body.data.scope === 'string') {
      if (generation !== stepUpGeneration || session !== sessionKey()) throw new DOMException('操作已取消', 'AbortError')
      const scope = body.data.scope as string
      requestScopes.set(key, scope)
      if (proof) forgetStepUpProof(scope, session)
      proof = await obtainStepUpProof(scope, session, opts.signal)
      if (opts.signal?.aborted) throw opts.signal.reason
      if (generation !== stepUpGeneration || session !== sessionKey()) throw new DOMException('操作已取消', 'AbortError')
      continue
    }
    throw responseError(path, res.status, body)
  }
  throw new Error('密码验证未完成')
}

async function request<T = any>(path: string, opts: RequestInit & ApiRequestOptions = {}, raw = false): Promise<T> {
  const { body } = await authorizedResponse(path, opts)
  return raw ? (body as T) : (body?.data ?? null)
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
  const { res, body: blob } = await authorizedResponse(path, options, true)
  // 优先用服务端给的文件名（Content-Disposition），它带了生成时间。
  let name = fallbackName
  const cd = res.headers.get('Content-Disposition') || ''
  const m = /filename="?([^"';]+)"?/.exec(cd)
  if (m) name = m[1]

  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻撤销会让部分浏览器取消尚未开始的下载，推迟一拍。
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}
