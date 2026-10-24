// Host/backend only. Do not import this module into the panel's browser page.
const RETRY_MS = [60_000, 120_000, 240_000, 300_000]

export function createQuotaPoller({ baseUrl, apiKey, onData, onStatus = () => {}, fetchImpl = globalThis.fetch }) {
  const key = typeof apiKey === 'string' ? apiKey.trim() : ''
  if (!key || /\s/.test(key)) throw new Error('A nonempty summary API key is required')
  if (typeof onData !== 'function' || typeof onStatus !== 'function' || typeof fetchImpl !== 'function') {
    throw new Error('Quota poller callbacks and fetch must be functions')
  }
  let endpoint
  try {
    endpoint = new URL(baseUrl)
    if (!['http:', 'https:'].includes(endpoint.protocol) || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) throw new Error()
    endpoint.pathname = endpoint.pathname.replace(/\/+$/, '') + '/api/v1/external/quota/summary'
  } catch {
    // Never reflect the supplied URL, which could contain accidental credentials.
    throw new Error('Keeper base URL must be an HTTP(S) URL without credentials, query or fragment')
  }

  let running = false
  let generation = 0
  let timer
  let controller
  let failures = 0
  let lastSuccessAt

  const emit = (state, message) => {
    // A UI status observer must not break the polling loop.
    try { onStatus({ state, ...(message ? { message } : {}), ...(lastSuccessAt ? { lastSuccessAt } : {}) }) } catch { /* observer error */ }
  }
  const isCurrent = (run) => running && generation === run

  async function poll(run) {
    if (!isCurrent(run)) return
    const requestController = new AbortController()
    controller = requestController
    const deadline = setTimeout(() => requestController.abort(), 10_000)
    try {
      const response = await fetchImpl(endpoint.href, {
        method: 'GET',
        headers: { Authorization: `Bearer ${key}`, Accept: 'application/json' },
        cache: 'no-store',
        redirect: 'error',
        signal: requestController.signal,
      })
      if (!isCurrent(run)) return
      if (response.status === 401) {
        running = false
        emit('unauthorized', '额度接口鉴权失败，请检查宿主中的只读密钥')
        return
      }
      if (!response.ok) throw new Error('quota request failed')
      const summary = await response.json()
      if (!summary || !Array.isArray(summary.groups)) throw new Error('invalid quota summary')
      if (!isCurrent(run)) return
      await onData(summary)
      if (!isCurrent(run)) return
      lastSuccessAt = new Date().toISOString()
      failures = 0
      emit('ok')
    } catch {
      if (!isCurrent(run)) return
      failures++
      // Ignore request/response error text: it may include a URL or Authorization.
      emit('error', '额度读取失败，保留上次成功数据并自动重试')
    } finally {
      clearTimeout(deadline)
      if (controller === requestController) controller = undefined
    }
    if (isCurrent(run)) {
      const delay = failures ? RETRY_MS[Math.min(failures - 1, RETRY_MS.length - 1)] : 60_000
      timer = setTimeout(() => { void poll(run) }, delay)
    }
  }

  return {
    start() {
      if (running) return
      running = true
      failures = 0
      emit('loading')
      return poll(++generation)
    },
    stop() {
      running = false
      generation++
      clearTimeout(timer)
      controller?.abort()
      emit('stopped')
    },
  }
}
