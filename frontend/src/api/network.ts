/** Options are additive: existing API callers keep working. No request retries. */
export interface ApiRequestOptions {
  signal?: AbortSignal | null
  timeoutMs?: number
}

// Covers headers AND response body consumption; longer than the server's 30s
// handler deadline so its error response can reach the UI. Zero disables it.
export async function withRequestDeadline<T>(
  options: ApiRequestOptions,
  run: (signal: AbortSignal) => Promise<T>,
): Promise<T> {
  const timeoutMs = options.timeoutMs ?? 45_000
  if (!Number.isFinite(timeoutMs) || timeoutMs < 0) throw new RangeError('Invalid API timeout')
  const controller = new AbortController()
  const parent = options.signal
  const cancel = () => controller.abort(parent?.reason)
  if (parent?.aborted) cancel()
  else parent?.addEventListener('abort', cancel, { once: true })
  const timer = timeoutMs > 0 ? setTimeout(() => {
    controller.abort(new DOMException('请求超时，请检查网络连接', 'TimeoutError'))
  }, timeoutMs) : undefined
  try {
    if (controller.signal.aborted) throw controller.signal.reason
    const value = await run(controller.signal)
    if (controller.signal.aborted) throw controller.signal.reason
    return value
  } catch (err) {
    if (controller.signal.aborted) throw controller.signal.reason
    throw err
  } finally {
    clearTimeout(timer)
    parent?.removeEventListener('abort', cancel)
  }
}
