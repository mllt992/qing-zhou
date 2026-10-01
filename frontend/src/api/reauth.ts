import { shallowRef } from 'vue'

export interface StepUpProof { proof: string; scope: string; expires_in: number }
interface CachedProof { proof: string; expiresAt: number }
interface Confirmation {
  id: number
  scope: string
  session: string
  key: string
  waiters: number
  promise: Promise<string>
  resolve: (proof: string) => void
  reject: (error: Error) => void
}

// Memory only. Never use local/session storage, URLs, cookies or console logs.
const proofs = new Map<string, CachedProof>()
const pending = new Map<string, Confirmation>()
const queue: Confirmation[] = []
let nextID = 0
export const stepUpPrompt = shallowRef<{ id: number; scope: string } | null>(null)
export let stepUpGeneration = 0

export const stepUpLabels: Record<string, string> = {
  'admin:backup': '下载或管理数据库备份',
  'admin:cert-export': '读取证书私钥或含密钥的节点配置',
  'admin:update': '安装更新或回滚',
  'admin:ssh': '修改服务器连接或 SSH 安全配置',
  'admin:security-settings': '修改系统安全配置或密钥',
  'admin:user-security': '修改用户账户或重置凭据',
  'user:subscription-reset': '更换订阅地址',
  'user:node-credentials-reset': '重置节点凭据',
}

function cacheKey(scope: string, session: string) { return JSON.stringify([session, scope]) }
export function cachedStepUpProof(scope: string, session: string): string | undefined {
  const key = cacheKey(scope, session)
  const cached = proofs.get(key)
  if (cached && cached.expiresAt > Date.now()) return cached.proof
  proofs.delete(key)
}
export function forgetStepUpProof(scope: string, session: string) { proofs.delete(cacheKey(scope, session)) }
function cancelled() { return new DOMException('已取消密码验证', 'AbortError') }
function activateNext() {
  const next = queue[0]
  stepUpPrompt.value = next ? { id: next.id, scope: next.scope } : null
}
function remove(job: Confirmation) {
  pending.delete(job.key)
  const index = queue.indexOf(job)
  if (index >= 0) queue.splice(index, 1)
  activateNext()
}
export function cancelStepUpPrompt(id: number) {
  const job = queue.find(candidate => candidate.id === id)
  if (!job) return
  remove(job)
  job.reject(cancelled())
}

// Navigation cancels pending actions but keeps still-valid proofs. Logout and
// password changes additionally clear proofs, even for cookie-only sessions.
export function cancelStepUpRequests(clearProofs = false) {
  stepUpGeneration++
  for (const job of [...queue]) cancelStepUpPrompt(job.id)
  if (clearProofs) proofs.clear()
}
export function finishStepUpPrompt(id: number, value: StepUpProof) {
  const job = queue.find(candidate => candidate.id === id)
  if (!job || stepUpPrompt.value?.id !== id) return
  if (value.scope !== job.scope || !value.proof || !Number.isFinite(value.expires_in) || value.expires_in <= 0) {
    remove(job)
    job.reject(new Error('密码验证响应无效'))
    return
  }
  // Allow for server expiry rounding and network delay. Never cache longer than 5m.
  proofs.set(job.key, { proof: value.proof, expiresAt: Date.now() + Math.max(0, Math.min(value.expires_in, 300) - 5) * 1000 })
  remove(job)
  job.resolve(value.proof)
}

export function obtainStepUpProof(scope: string, session: string, signal?: AbortSignal | null): Promise<string> {
  if (signal?.aborted) return Promise.reject(signal.reason ?? cancelled())
  if (!Object.prototype.hasOwnProperty.call(stepUpLabels, scope)) return Promise.reject(new Error('不支持的二次验证范围'))
  const cached = cachedStepUpProof(scope, session)
  if (cached) return Promise.resolve(cached)
  const key = cacheKey(scope, session)
  let job = pending.get(key)
  if (!job) {
    let resolve!: Confirmation['resolve'], reject!: Confirmation['reject']
    const promise = new Promise<string>((yes, no) => { resolve = yes; reject = no })
    job = { id: ++nextID, key, scope, session, waiters: 0, promise, resolve, reject }
    pending.set(key, job)
    queue.push(job)
    activateNext()
  }
  const waiting = job
  waiting.waiters++
  return new Promise((resolve, reject) => {
    let finished = false
    const finish = (value?: string, error?: unknown) => {
      if (finished) return
      finished = true
      signal?.removeEventListener('abort', abort)
      waiting.waiters--
      if (error) reject(error)
      else resolve(value!)
    }
    const abort = () => {
      finish(undefined, signal?.reason ?? cancelled())
      if (!waiting.waiters && pending.has(waiting.key)) cancelStepUpPrompt(waiting.id)
    }
    signal?.addEventListener('abort', abort, { once: true })
    waiting.promise.then(value => finish(value), error => finish(undefined, error))
  })
}
