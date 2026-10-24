import test from 'node:test'
import assert from 'node:assert/strict'
import {
  formatCompactNumber,
  formatUsdRemaining,
  formatRate,
  calculateCacheReadRate,
  formatCacheReadRate,
  formatPlanBadge,
  formatResetCountdown,
  formatShanghaiDate,
  formatCoverageInfo,
  getAppleRingPalette,
  calculateDashOffset,
  adaptSummaryResponse,
  OUTER_CIRCUMFERENCE,
  INNER_CIRCUMFERENCE,
  WINDOW_5H_SECONDS,
  WINDOW_1W_SECONDS,
} from './panel.mjs'

test('formatCompactNumber handles edge cases, 0, and suffixes', () => {
  assert.equal(formatCompactNumber(0), '0')
  assert.equal(formatCompactNumber(249), '249')
  assert.equal(formatCompactNumber(10420), '10.42K')
  assert.equal(formatCompactNumber(1340000000), '1.34B')
  assert.equal(formatCompactNumber(945740000), '945.74M')
  assert.equal(formatCompactNumber(null), '—')
  assert.equal(formatCompactNumber(undefined), '—')
  assert.equal(formatCompactNumber(NaN), '—')
})

test('formatUsdRemaining computes max(est - actual, 0) and formats correctly', () => {
  // est = 10, act = 2 => $8.00
  assert.equal(formatUsdRemaining(10, 2), '$8.00')
  // est = 5, act = 10 => $0.00 (clamped)
  assert.equal(formatUsdRemaining(5, 10), '$0.00')
  // est = 0, act = 0 => $0.00
  assert.equal(formatUsdRemaining(0, 0), '$0.00')
  // missing either value => '—'
  assert.equal(formatUsdRemaining(null, 5), '—')
  assert.equal(formatUsdRemaining(10, null), '—')
  assert.equal(formatUsdRemaining(undefined, undefined), '—')
})

test('formatRate computes percentage safely with 100% clamp', () => {
  assert.equal(formatRate(10170, 10420), '97.60%')
  assert.equal(formatRate(0, 100), '0.00%')
  assert.equal(formatRate(100, 0), '—')
  assert.equal(formatRate(50, null), '—')
  assert.equal(formatRate(null, 50), '—')
  assert.equal(formatRate(120, 100), '100.00%')
})

test('calculateCacheReadRate and formatCacheReadRate allow >100% without clamp (Keeper parity)', () => {
  assert.equal(calculateCacheReadRate(800000, 1000000), 80)
  assert.equal(formatCacheReadRate(800000, 1000000), '80.00%')
  // > 100% test case
  assert.equal(calculateCacheReadRate(150000, 100000), 150)
  assert.equal(formatCacheReadRate(150000, 100000), '150.00%')
  // Edge cases
  assert.equal(calculateCacheReadRate(0, 100), 0)
  assert.equal(formatCacheReadRate(0, 100), '0.00%')
  assert.equal(calculateCacheReadRate(100, 0), null)
  assert.equal(formatCacheReadRate(100, 0), '—')
  assert.equal(calculateCacheReadRate(null, 100), null)
  assert.equal(formatCacheReadRate(null, 100), '—')
})

test('formatPlanBadge formats all plans with count including 1', () => {
  assert.equal(formatPlanBadge([{ plan: 'Team', count: 3 }]), 'Team ×3')
  assert.equal(formatPlanBadge([{ plan: 'Pro', count: 1 }]), 'Pro ×1')
  assert.equal(
    formatPlanBadge([
      { plan: 'Team', count: 2 },
      { plan: 'Pro', count: 1 },
    ]),
    'Team ×2, Pro ×1'
  )
  assert.equal(formatPlanBadge([]), '')
  assert.equal(formatPlanBadge(null), '')
})

test('formatCoverageInfo formats separate retained and stale without merging or NaN', () => {
  const cov = {
    eligible: 6,
    current: 4,
    peer: 2,
    samples: 10,
    historical: 1,
    retained: 2,
    stale: 1,
    expired: 1,
  }
  const text = formatCoverageInfo(cov, 'cached', 'cached')
  assert.ok(text.includes('覆盖凭证: 4/6'))
  assert.ok(text.includes('同行对齐: 2 (样本: 10)'))
  assert.ok(text.includes('历史估值: 1'))
  assert.ok(text.includes('缓存保留: 2'))
  assert.ok(text.includes('过旧数据: 1'))
  assert.ok(text.includes('跨周期: 1'))
  assert.ok(text.includes('用量状态: cached'))
  assert.ok(text.includes('估算状态: cached'))
  assert.ok(!text.includes('NaN'))

  // Partial coverage without retained/stale
  const emptyCov = { eligible: 2, current: 2 }
  assert.equal(formatCoverageInfo(emptyCov, 'current', 'current'), '覆盖凭证: 2/2')
})

test('formatResetCountdown and formatShanghaiDate format time correctly', () => {
  const now = new Date('2026-10-24T12:00:00Z').getTime()
  // 32 minutes later
  const target32m = new Date('2026-10-24T12:32:00Z').toISOString()
  assert.equal(formatResetCountdown(target32m, now), '32m')

  // 4 hours 16 minutes later
  const target4h = new Date('2026-10-24T16:16:00Z').toISOString()
  assert.equal(formatResetCountdown(target4h, now), '4h16m')

  // 6 days 14 hours later
  const target6d = new Date('2026-10-31T02:00:00Z').toISOString()
  assert.equal(formatResetCountdown(target6d, now), '6d14h')

  // past reset -> 0m
  const past = new Date('2026-10-24T11:00:00Z').toISOString()
  assert.equal(formatResetCountdown(past, now), '0m')

  assert.equal(formatResetCountdown(null, now), '—')

  // Shanghai timezone check: 2026-10-24T12:00:00Z is 20:00 in UTC+8
  assert.equal(formatShanghaiDate('2026-10-24T12:00:00Z'), '10/24 20:00')
  assert.equal(formatShanghaiDate(null), '—')
})

test('getAppleRingPalette selects apple green / amber / red / unknown', () => {
  assert.equal(getAppleRingPalette(71).textClass, 'text-emerald-400')
  assert.equal(getAppleRingPalette(60).textClass, 'text-emerald-400')
  assert.equal(getAppleRingPalette(59).textClass, 'text-amber-400')
  assert.equal(getAppleRingPalette(25).textClass, 'text-amber-400')
  assert.equal(getAppleRingPalette(24).textClass, 'text-rose-400')
  assert.equal(getAppleRingPalette(0).textClass, 'text-rose-400')
  assert.equal(getAppleRingPalette(null).textClass, 'text-zinc-500')
  assert.equal(getAppleRingPalette(null).isUnknown, true)
})

test('calculateDashOffset preserves exact 0 and unknown as circumference without fake arcs', () => {
  assert.equal(calculateDashOffset(OUTER_CIRCUMFERENCE, 100), 0)
  assert.equal(calculateDashOffset(OUTER_CIRCUMFERENCE, 0), OUTER_CIRCUMFERENCE)
  assert.equal(calculateDashOffset(OUTER_CIRCUMFERENCE, null), OUTER_CIRCUMFERENCE)
  assert.equal(calculateDashOffset(INNER_CIRCUMFERENCE, 50), INNER_CIRCUMFERENCE * 0.5)
})

test('adaptSummaryResponse strictly matches codex+codex and antigravity+antigravity-gemini-models', () => {
  const sampleSummary = {
    generated_at: '2026-10-24T15:00:00Z',
    eligible: 6,
    pending: 0,
    groups: [
      {
        provider: 'codex',
        pool: 'codex',
        label: 'Codex / GPT',
        plans: [{ plan: 'Team', count: 3 }],
        stats: {
          total_requests: 10420,
          success_count: 10170,
          failure_count: 249,
          input_tokens: 1000000,
          cache_read_tokens: 800000,
          total_tokens: 1340000000,
        },
        quota: [
          {
            key: 'summary.codex.18000',
            remainingFraction: 0.71,
            window_usage_cost: 3.46,
            window_usage_estimate_cost: 12.0,
            coverage: { next_reset_at: '2026-10-24T15:32:00Z', eligible: 3, current: 3 },
            window: { seconds: WINDOW_5H_SECONDS },
          },
          {
            key: 'summary.codex.604800',
            remainingFraction: 0.95,
            window_usage_cost: 1.91,
            window_usage_estimate_cost: 12.0,
            coverage: { next_reset_at: '2026-10-31T05:50:00Z', eligible: 3, current: 3 },
            window: { seconds: WINDOW_1W_SECONDS },
          },
        ],
      },
      // Pool with antigravity-gemini-models should match
      {
        provider: 'antigravity',
        pool: 'antigravity-gemini-models',
        label: 'Gemini Models',
        plans: [{ plan: 'Pro', count: 3 }],
        stats: {
          total_requests: 20900,
          success_count: 20030,
          failure_count: 870,
          input_tokens: 500000,
          cache_read_tokens: 372500,
          total_tokens: 945740000,
        },
        quota: [
          {
            key: 'summary.antigravity-gemini-models.18000',
            remainingFraction: 0.52,
            window_usage_cost: 5.62,
            window_usage_estimate_cost: 11.8,
            coverage: { next_reset_at: '2026-10-24T19:39:00Z', eligible: 3, current: 2, peer: 1, samples: 4 },
            window: { seconds: WINDOW_5H_SECONDS },
          },
          {
            key: 'summary.antigravity-gemini-models.604800',
            remainingFraction: 0.48,
            window_usage_cost: 1.0,
            window_usage_estimate_cost: 30.0,
            coverage: { next_reset_at: '2026-10-30T19:50:00Z', eligible: 3, current: 3 },
            window: { seconds: WINDOW_1W_SECONDS },
          },
        ],
      },
      // Pool with antigravity-claude-gpt-models must NOT be matched for AG Gemini column
      {
        provider: 'antigravity',
        pool: 'antigravity-claude-gpt-models',
        label: 'Claude / GPT Models',
        plans: [{ plan: 'Pro', count: 2 }],
        quota: [],
      },
      // Pool named 'gemini' must NOT be matched
      {
        provider: 'antigravity',
        pool: 'gemini',
        label: 'Wrong Gemini Pool',
        quota: [],
      },
    ],
  }

  const model = adaptSummaryResponse(sampleSummary, new Date('2026-10-24T15:00:00Z').getTime())

  assert.ok(model.codex)
  assert.equal(model.codex.badge, 'Team ×3')
  assert.equal(model.codex.requestsTotal, '10.42K')
  assert.equal(model.codex.requestsOk, '10.17K')
  assert.equal(model.codex.requestsErr, '249')
  assert.equal(model.codex.fiveHour.percent, 71)
  assert.equal(model.codex.fiveHour.usd, '$8.54')
  assert.equal(model.codex.fiveHour.resetLeft, '32m')
  assert.equal(model.codex.weekly.percent, 95)
  assert.equal(model.codex.weekly.usd, '$10.09')
  assert.equal(model.codex.cacheHitRate, '80.00%')

  assert.ok(model.antigravity)
  assert.equal(model.antigravity.name, 'Gemini Models')
  assert.equal(model.antigravity.badge, 'Pro ×3')
  assert.equal(model.antigravity.fiveHour.percent, 52)
  assert.equal(model.antigravity.fiveHour.usd, '$6.18')
  assert.equal(model.antigravity.fiveHour.hasPartialCoverage, true)
  assert.ok(model.antigravity.fiveHour.coverageInfo.includes('同行对齐: 1'))
  assert.equal(model.antigravity.weekly.percent, 48)
  assert.equal(model.antigravity.weekly.usd, '$29.00')
  assert.equal(model.antigravity.cacheHitRate, '74.50%')
})

test('missing or partial groups in summary clears column without leaving stale data', () => {
  const emptySummary = {
    generated_at: '2026-10-24T16:00:00Z',
    eligible: 0,
    pending: 0,
    groups: [],
  }
  const model = adaptSummaryResponse(emptySummary)
  assert.equal(model.codex, null)
  assert.equal(model.antigravity, null)

  const onlyCodexSummary = {
    generated_at: '2026-10-24T16:00:00Z',
    eligible: 1,
    pending: 0,
    groups: [
      {
        provider: 'codex',
        pool: 'codex',
        label: 'Codex / GPT',
        plans: [{ plan: 'Free', count: 1 }],
        quota: [],
      },
    ],
  }
  const partialModel = adaptSummaryResponse(onlyCodexSummary)
  assert.ok(partialModel.codex)
  assert.equal(partialModel.antigravity, null)
})
