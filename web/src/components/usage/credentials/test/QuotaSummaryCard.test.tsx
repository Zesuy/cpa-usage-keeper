// @vitest-environment happy-dom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import type { QuotaSummaryResponse } from '@/lib/types'
import { defaultAntigravityQuotaDisplayPreferences, useAntigravityQuotaDisplayStore } from '@/stores/useAntigravityQuotaDisplayStore'
import { QuotaSummaryCard } from '../QuotaSummaryCard'

globalThis.IS_REACT_ACT_ENVIRONMENT = true
const fetchSummary = vi.fn()
const refreshSummary = vi.fn()
vi.mock('@/lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/api')>(),
  fetchQuotaSummary: (...args: unknown[]) => fetchSummary(...args),
  refreshQuotaSummary: () => refreshSummary(),
}))
vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key }),
}))

const data: QuotaSummaryResponse = {
  generated_at: '2026-10-23T07:00:00Z', eligible: 3, pending: 0,
  groups: [{
    provider: 'codex',
    pool: 'codex',
    label: 'Codex / GPT',
    stats: {
      total_requests: 98,
      success_count: 52,
      failure_count: 46,
      input_tokens: 980,
      cache_read_tokens: 160,
      total_tokens: 1120,
    },
    quota: [
      {
        key: 'summary.codex.18000', scope: 'window', label: '5h', window: { seconds: 18000 }, remainingFraction: .8,
        window_usage_tokens: 1_000_000, window_usage_cost: 2,
        window_usage_estimate_tokens: 5_000_000, window_usage_estimate_cost: 10,
        coverage: { eligible: 3, current: 3, direct: 1, historical: 1, peer: 1, unknown: 0, missing: 0, stale: 0, failed: 0, pending: 0, unsupported: 0, samples: 2, oldest_estimate_at: '2026-10-22T08:00:00Z' },
      },
      {
        key: 'summary.codex.604800', scope: 'window', label: 'Weekly', window: { seconds: 604800 }, remainingFraction: .5,
        window_usage_tokens: 4_000_000, window_usage_cost: 8,
        window_usage_estimate_tokens: 12_000_000, window_usage_estimate_cost: 24,
        coverage: { eligible: 3, current: 1, direct: 1, historical: 0, peer: 0, unknown: 2, missing: 0, stale: 0, failed: 0, pending: 0, unsupported: 0, samples: 1, oldest_estimate_at: '2026-10-20T10:00:00Z' },
      },
    ],
  }],
}

describe('QuotaSummaryCard', () => {
  let container: HTMLDivElement
  let root: Root
  beforeEach(() => {
    vi.clearAllMocks()
    fetchSummary.mockResolvedValue(structuredClone(data))
    refreshSummary.mockResolvedValue({ tasks: [], rejected: [], accepted: 0, skipped: 0, limit: 0 })
    useAntigravityQuotaDisplayStore.setState({ groups: defaultAntigravityQuotaDisplayPreferences() })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    vi.useRealTimers()
  })
  const render = async (onAuthRequired?: () => void) => {
    await act(async () => root.render(<QuotaSummaryCard timeZone="Asia/Shanghai" onAuthRequired={onAuthRequired} />))
  }
  const button = (text: string) => [...container.querySelectorAll<HTMLButtonElement>('button')].find((item) => item.textContent?.includes(text))!

  it('reuses current/estimated controls and quota bars without refreshing upstream on mount', async () => {
    await render()
    expect(container.textContent).toContain('Codex / GPT')
    expect(container.textContent).toContain('80%')
    expect(container.textContent).toContain('1.00M')
    expect(container.textContent).toContain('$2.00')
    expect(container.textContent).not.toContain('5.00M')
    expect(refreshSummary).not.toHaveBeenCalled()
    await act(async () => button('credentials_quota_usage_mode_estimated').click())
    expect(container.textContent).toContain('5.00M')
    expect(container.textContent).toContain('$10.00')
    expect(container.textContent).toContain('80%')
    expect(container.textContent).toContain('quota_summary_filled')
    expect(container.querySelectorAll('details').length).toBe(2)
    // Aggregates deliberately have no fictitious common reset countdown.
    expect(container.textContent).not.toContain('NaN')
  })

  it('renders credential metrics identically to credential rows and handles missing stats gracefully', async () => {
    await render()
    // 渲染表头
    expect(container.textContent).toContain('usage_stats.total_requests')
    expect(container.textContent).toContain('usage_stats.success_rate')
    expect(container.textContent).toContain('usage_stats.total_tokens')
    expect(container.textContent).toContain('usage_stats.cache_rate')

    // 验证指标数值与计算
    // total_requests: 98, success: 52, failure: 46
    expect(container.textContent).toContain('98')
    expect(container.textContent).toContain('52')
    expect(container.textContent).toContain('46')
    // success_rate: 52/98 = 53.06%
    expect(container.textContent).toContain('53.06%')
    // total_tokens: 1120 -> 1.12K
    expect(container.textContent).toContain('1.12K')
    // cache_read_rate: 160/980 = 16.33%
    expect(container.textContent).toContain('16.33%')
    // 范围说明 tooltip
    expect(container.querySelector('[title="usage_stats.quota_summary_metrics_scope"]')).not.toBeNull()

    // 兼容缺少 stats 字段的旧结构
    await act(async () => root.unmount())
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    const withoutStats = structuredClone(data)
    delete withoutStats.groups[0].stats
    fetchSummary.mockResolvedValue(withoutStats)
    await render()
    expect(container.textContent).toContain('—')
  })

  it('keeps independent coverage statistics and sources for each window bar', async () => {
    await render()
    // 5h 覆盖 3/3，Weekly 覆盖 1/3
    expect(container.textContent).toContain('"covered":3,"total":3')
    expect(container.textContent).toContain('"covered":1,"total":3')

    await act(async () => button('credentials_quota_usage_mode_estimated').click())
    // 检查不同窗口的 details 和格式化后的历史日期都独立存在
    const detailsElements = container.querySelectorAll('details')
    expect(detailsElements.length).toBe(2)
    expect(detailsElements[0].textContent).toContain('10/22 16:00')
    expect(detailsElements[1].textContent).toContain('10/20 18:00')
  })

  it('does not fall back to actual spend when aggregate capacity is unknown', async () => {
    const unknown = structuredClone(data)
    delete unknown.groups[0].quota[0].window_usage_estimate_tokens
    delete unknown.groups[0].quota[0].window_usage_estimate_cost
    fetchSummary.mockResolvedValue(unknown)
    await render()
    await act(async () => button('credentials_quota_usage_mode_estimated').click())
    expect(container.textContent).not.toContain('$2.00')
    expect(container.textContent).toContain('quota_summary_unknown')
  })

  it.each([0, 2])('retains cached amounts and observation time with %i pending refreshes', async (pending) => {
    const cached = structuredClone(data)
    cached.pending = pending
    for (const row of cached.groups[0].quota) {
      row.usage_state = 'cached'
      row.estimate_state = 'cached'
      row.coverage!.retained = 3
      row.coverage!.stale = 3
      row.coverage!.oldest_snapshot_at = '2026-10-23T06:15:00Z'
      row.coverage!.pending = pending
      row.coverage!.failed = pending ? 0 : 1
    }
    fetchSummary.mockResolvedValue(cached)
    await render()
    expect(container.textContent).toContain('80%')
    expect(container.textContent).toContain('1.00M')
    expect(container.textContent).toContain('quota_summary_cached')
    expect(container.querySelector('time')?.dateTime).toBe('2026-10-23T06:15:00Z')
    expect(container.querySelector('time')?.textContent).toBe('10/23 14:15')
    expect(button(pending ? 'quota_summary_pending' : 'quota_summary_refresh_all').disabled).toBe(Boolean(pending))
    await act(async () => button('credentials_quota_usage_mode_estimated').click())
    expect(container.textContent).toContain('5.00M')
    expect(container.textContent).toContain('quota_summary_cached')
  })

  it.each(['reset', 'pending', 'failed', 'missing', 'missing_usage'] as const)('explains %s without showing obsolete window data', async (state) => {
    const unavailable = structuredClone(data)
    const row = unavailable.groups[0].quota[0]
    delete row.window_usage_tokens
    delete row.window_usage_cost
    delete row.window_usage_estimate_tokens
    delete row.window_usage_estimate_cost
    delete row.remainingFraction
    row.usage_state = state
    row.estimate_state = state
    row.coverage!.current = 0
    row.coverage!.direct = 0
    row.coverage!.historical = 0
    row.coverage!.peer = 0
    fetchSummary.mockResolvedValue(unavailable)
    await render()
    expect(container.textContent).toContain(`quota_summary_state_${state}`)
    expect(container.textContent).not.toContain('80%')
    expect(container.textContent).not.toContain('1.00M')
    expect(container.textContent).not.toContain('$2.00')
    // Weekly remains available independently of the expired/missing 5h window.
    expect(container.textContent).toContain('4.00M')
    await act(async () => button('credentials_quota_usage_mode_estimated').click())
    expect(container.textContent).toContain(`quota_summary_state_${state}`)
    expect(container.textContent).not.toContain('5.00M')
    expect(container.textContent).toContain('12.00M')
  })

  it('distinguishes a real zero from insufficient estimate samples', async () => {
    const zero = structuredClone(data)
    const row = zero.groups[0].quota[0]
    row.window_usage_tokens = 0
    row.window_usage_cost = 0
    row.usage_state = 'current'
    row.estimate_state = 'insufficient_samples'
    delete row.window_usage_estimate_tokens
    delete row.window_usage_estimate_cost
    delete row.remainingFraction
    fetchSummary.mockResolvedValue(zero)
    await render()
    expect(container.textContent).toContain('$0.00')
    expect(container.textContent).not.toContain('quota_summary_state_insufficient_samples')
    await act(async () => button('credentials_quota_usage_mode_estimated').click())
    expect(container.textContent).toContain('quota_summary_state_insufficient_samples')
    expect(container.textContent).not.toContain('$0.00')
  })

  it('refreshes the complete summary scope once and shows progress', async () => {
    await render()
    fetchSummary.mockResolvedValue({ ...data, pending: 3 })
    await act(async () => button('quota_summary_refresh_all').click())
    expect(refreshSummary).toHaveBeenCalledTimes(1)
    expect(button('quota_summary_pending').disabled).toBe(true)
    await act(async () => button('quota_summary_pending').click())
    expect(refreshSummary).toHaveBeenCalledTimes(1)
  })

  it('polls pending tasks every five seconds and switches to a minute when idle', async () => {
    vi.useFakeTimers()
    fetchSummary.mockResolvedValue({ ...data, pending: 2 })
    await render()
    const initialCalls = fetchSummary.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(fetchSummary.mock.calls.length).toBe(initialCalls + 1)
    fetchSummary.mockResolvedValue(data)
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    const idleCalls = fetchSummary.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(59_000) })
    expect(fetchSummary.mock.calls.length).toBe(idleCalls)
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
    expect(fetchSummary.mock.calls.length).toBe(idleCalls + 1)
  })

  it('applies existing Antigravity visibility choices without changing API data', async () => {
    const ag = structuredClone(data)
    ag.groups = [{ ...ag.groups[0], provider: 'antigravity', pool: 'antigravity-gemini-models', label: 'Gemini Models' }]
    fetchSummary.mockResolvedValue(ag)
    await render()
    expect(container.textContent).toContain('Gemini Models')
    await act(async () => useAntigravityQuotaDisplayStore.getState().setGroupVisible('gemini', false))
    expect(container.textContent).not.toContain('Gemini Models')
    expect(container.textContent).toContain('antigravity_quota_display_hidden')
    expect(ag.groups).toHaveLength(1)
  })

  it('shows auth errors and aborts outstanding reads on unmount', async () => {
    fetchSummary.mockRejectedValue(new ApiError('expired', 401))
    const onAuthRequired = vi.fn()
    await render(onAuthRequired)
    expect(onAuthRequired).toHaveBeenCalledOnce()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('expired')
    const signal = fetchSummary.mock.calls[0][0] as AbortSignal
    await act(async () => root.render(null))
    expect(signal.aborted).toBe(true)
  })
})
