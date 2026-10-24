// AccountQuotaPanel core logic & data adapter
// Pure functions for converting SummaryResponse into view model and managing panel state.

export const WINDOW_5H_SECONDS = 18_000;
export const WINDOW_1W_SECONDS = 604_800;

export const OUTER_CIRCUMFERENCE = 2 * Math.PI * 56; // 351.85837
export const INNER_CIRCUMFERENCE = 2 * Math.PI * 40; // 251.32741

/**
 * Format a number into compact representation (e.g. 1.34B, 10.42K, 249).
 */
export function formatCompactNumber(value) {
  if (value === null || value === undefined || !Number.isFinite(Number(value))) {
    return '—';
  }
  const num = Number(value);
  const abs = Math.abs(num);
  const formatScaled = (scaled, suffix) => `${scaled.toFixed(2)}${suffix}`;

  if (abs < 1_000) {
    return new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 }).format(num);
  }
  if (abs < 1_000_000) {
    return formatScaled(num / 1_000, 'K');
  }
  if (abs < 1_000_000_000) {
    return formatScaled(num / 1_000_000, 'M');
  }
  return formatScaled(num / 1_000_000_000, 'B');
}

/**
 * Calculate and format USD remaining: max(estimate_cost - cost, 0)
 * If either estimate_cost or cost is missing/null/undefined, returns '—'.
 * If calculation succeeds and result is 0, returns '$0.00'.
 */
export function formatUsdRemaining(estimateCost, cost) {
  if (
    estimateCost === null ||
    estimateCost === undefined ||
    cost === null ||
    cost === undefined ||
    !Number.isFinite(Number(estimateCost)) ||
    !Number.isFinite(Number(cost))
  ) {
    return '—';
  }
  const est = Number(estimateCost);
  const act = Number(cost);
  const diff = Math.max(0, est - act);
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(diff);
}

/**
 * Format general percentage rate clamped to [0, 100]. If denominator is missing or <= 0, returns '—'.
 */
export function formatRate(numerator, denominator) {
  if (
    numerator === null ||
    numerator === undefined ||
    denominator === null ||
    denominator === undefined ||
    !Number.isFinite(Number(numerator)) ||
    !Number.isFinite(Number(denominator))
  ) {
    return '—';
  }
  const num = Number(numerator);
  const den = Number(denominator);
  if (den <= 0) {
    return '—';
  }
  const rate = Math.max(0, Math.min(100, (num / den) * 100));
  return `${rate.toFixed(2)}%`;
}

/**
 * Calculate cache read rate strictly matching Keeper web/src/utils/usage.ts:162 calculateCacheReadRate.
 * Returns (Math.max(cacheRead, 0) / Math.max(input, 0)) * 100 with NO 100% upper clamp.
 * If denominator <= 0, returns null.
 */
export function calculateCacheReadRate(cacheReadTokens, inputTokens) {
  if (
    cacheReadTokens === null ||
    cacheReadTokens === undefined ||
    inputTokens === null ||
    inputTokens === undefined ||
    !Number.isFinite(Number(cacheReadTokens)) ||
    !Number.isFinite(Number(inputTokens))
  ) {
    return null;
  }
  const input = Math.max(Number(inputTokens), 0);
  const cacheRead = Math.max(Number(cacheReadTokens), 0);
  if (input <= 0) {
    return null;
  }
  return (cacheRead / input) * 100;
}

/**
 * Format cache read rate into display string without upper 100% clamping.
 */
export function formatCacheReadRate(cacheReadTokens, inputTokens) {
  const rate = calculateCacheReadRate(cacheReadTokens, inputTokens);
  if (rate === null || !Number.isFinite(rate)) {
    return '—';
  }
  return `${rate.toFixed(2)}%`;
}

/**
 * Format plan badge string: e.g. "Team ×3", "Pro ×3", "Free ×1" or "Team ×2, Pro ×1".
 * Returns empty string if no plans.
 */
export function formatPlanBadge(plans) {
  if (!Array.isArray(plans) || plans.length === 0) {
    return '';
  }
  return plans
    .map((p) => {
      const name = p.plan || 'Standard';
      const count = p.count ?? 1;
      return `${name} ×${count}`;
    })
    .join(', ');
}

/**
 * Format next_reset_at countdown: e.g. "0h32m", "6d14h", "0m".
 * If nextResetAt is missing or invalid, returns '—'.
 */
export function formatResetCountdown(nextResetAt, nowMs = Date.now()) {
  if (!nextResetAt) return '—';
  const target = new Date(nextResetAt).getTime();
  if (isNaN(target)) return '—';
  const diffMs = target - nowMs;
  if (diffMs <= 0) {
    return '0m';
  }
  const totalMinutes = Math.floor(diffMs / 60_000);
  const days = Math.floor(totalMinutes / (24 * 60));
  const hours = Math.floor((totalMinutes % (24 * 60)) / 60);
  const minutes = totalMinutes % 60;

  if (days > 0) {
    return `${days}d${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h${minutes}m`;
  }
  return `${minutes}m`;
}

/**
 * Format date in Asia/Shanghai timezone: MM/DD HH:mm
 */
export function formatShanghaiDate(dateString) {
  if (!dateString) return '—';
  const date = new Date(dateString);
  if (isNaN(date.getTime())) return '—';

  const formatter = new Intl.DateTimeFormat('en-US', {
    timeZone: 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
  const parts = formatter.formatToParts(date);
  const getPart = (type) => parts.find((p) => p.type === type)?.value || '';
  return `${getPart('month')}/${getPart('day')} ${getPart('hour')}:${getPart('minute')}`;
}

/**
 * Build a concise coverage tooltip describing sample completeness, peer fill, cache state and reset.
 * Avoids merging retained and stale since they can overlap; lists raw fields safely.
 */
export function formatCoverageInfo(coverage, usageState, estimateState) {
  if (!coverage) return '';
  const parts = [];
  if (coverage.eligible !== undefined) {
    const current = coverage.current ?? 0;
    const eligible = coverage.eligible ?? 0;
    parts.push(`覆盖凭证: ${current}/${eligible}`);
  }
  if (coverage.peer > 0) {
    parts.push(`同行对齐: ${coverage.peer} (样本: ${coverage.samples ?? 0})`);
  }
  if (coverage.historical > 0) {
    parts.push(`历史估值: ${coverage.historical}`);
  }
  if (coverage.retained > 0) {
    parts.push(`缓存保留: ${coverage.retained}`);
  }
  if (coverage.stale > 0) {
    parts.push(`过旧数据: ${coverage.stale}`);
  }
  if (coverage.expired > 0) {
    parts.push(`跨周期: ${coverage.expired}`);
  }
  if (usageState && usageState !== 'current') {
    parts.push(`用量状态: ${usageState}`);
  }
  if (estimateState && estimateState !== 'current') {
    parts.push(`估算状态: ${estimateState}`);
  }
  return parts.join(' · ');
}

/**
 * Return Apple Activity Ring palette for a given remaining fraction / percent.
 * fraction: number [0, 1] or null/undefined
 */
export function getAppleRingPalette(percent) {
  if (percent === null || percent === undefined || isNaN(percent)) {
    // Neutral grey state
    return {
      stop1: '#636366',
      stop2: '#48484a',
      track: 'rgba(255, 255, 255, 0.08)',
      textClass: 'text-zinc-500',
      dotBg: 'bg-zinc-600',
      borderColor: 'rgba(255, 255, 255, 0.12)',
      glow: 'transparent',
      isCritical: false,
      isUnknown: true,
    };
  }
  if (percent >= 60) {
    return {
      stop1: '#30D158',
      stop2: '#00F59B',
      track: 'rgba(48, 209, 88, 0.14)',
      textClass: 'text-emerald-400',
      dotBg: 'bg-emerald-400',
      borderColor: 'rgba(48, 209, 88, 0.28)',
      glow: '#30D158',
      isCritical: false,
      isUnknown: false,
    };
  }
  if (percent >= 25) {
    return {
      stop1: '#FFD60A',
      stop2: '#FF9F0A',
      track: 'rgba(255, 214, 10, 0.14)',
      textClass: 'text-amber-400',
      dotBg: 'bg-amber-400',
      borderColor: 'rgba(255, 159, 10, 0.3)',
      glow: '#FF9F0A',
      isCritical: false,
      isUnknown: false,
    };
  }
  return {
    stop1: '#FF453A',
    stop2: '#FF2D55',
    track: 'rgba(255, 69, 58, 0.16)',
    textClass: 'text-rose-400',
    dotBg: 'bg-rose-400',
    borderColor: 'rgba(255, 69, 58, 0.4)',
    glow: '#FF453A',
    isCritical: true,
    isUnknown: false,
  };
}

/**
 * Calculates stroke-dashoffset for a circle.
 * If percent is null or unknown, returns circumference (0% arc).
 * If percent is 0, returns circumference (exact 0, no fake arc).
 */
export function calculateDashOffset(circumference, percent) {
  if (percent === null || percent === undefined || isNaN(percent) || percent <= 0) {
    return circumference;
  }
  const clamped = Math.min(100, percent);
  return circumference * (1 - clamped / 100);
}

/**
 * Extract window quota data from a group.
 */
function extractWindowQuota(quotaRows, seconds, nowMs) {
  const row = (quotaRows || []).find(
    (r) => r.window && r.window.seconds === seconds
  );

  if (!row) {
    return {
      percent: null,
      percentText: '—',
      usd: '—',
      resetLeft: '—',
      resetAt: '—',
      nextResetAt: null,
      coverageInfo: '',
      hasPartialCoverage: false,
    };
  }

  let percent = null;
  let percentText = '—';
  if (row.remainingFraction !== null && row.remainingFraction !== undefined && Number.isFinite(Number(row.remainingFraction))) {
    percent = Math.round(Math.max(0, Math.min(1, Number(row.remainingFraction))) * 100);
    percentText = `${percent}%`;
  }

  const usd = formatUsdRemaining(row.window_usage_estimate_cost, row.window_usage_cost);
  const nextReset = row.coverage?.next_reset_at || null;
  const resetLeft = formatResetCountdown(nextReset, nowMs);
  const resetAt = formatShanghaiDate(nextReset);
  const coverageInfo = formatCoverageInfo(row.coverage, row.usage_state, row.estimate_state);
  const hasPartialCoverage = Boolean(row.coverage && row.coverage.eligible > 0 && row.coverage.current < row.coverage.eligible);

  return {
    percent,
    percentText,
    usd,
    resetLeft,
    resetAt,
    nextResetAt: nextReset,
    coverage: row.coverage || null,
    usageState: row.usage_state || '',
    estimateState: row.estimate_state || '',
    coverageInfo,
    hasPartialCoverage,
  };
}

/**
 * Adapt a SummaryGroup into view model for one provider column.
 */
export function adaptSummaryGroup(group, nowMs = Date.now()) {
  if (!group) {
    return null;
  }
  const badge = formatPlanBadge(group.plans);
  const stats = group.stats || {};

  const totalRequests = stats.total_requests ?? null;
  const successCount = stats.success_count ?? null;
  const failureCount = stats.failure_count ?? null;
  const inputTokens = stats.input_tokens ?? null;
  const cacheReadTokens = stats.cache_read_tokens ?? null;
  const totalTokens = stats.total_tokens ?? null;

  const requestsTotal = formatCompactNumber(totalRequests);
  const requestsOk = formatCompactNumber(successCount);
  const requestsErr = formatCompactNumber(failureCount);
  const successRate = formatRate(successCount, totalRequests);

  const cumulativeTokens = formatCompactNumber(totalTokens);
  // Cache read rate strictly following Keeper calculateCacheReadRate (no 100% clamp)
  const cacheHitRate = formatCacheReadRate(cacheReadTokens, inputTokens);

  const fiveHour = extractWindowQuota(group.quota, WINDOW_5H_SECONDS, nowMs);
  const weekly = extractWindowQuota(group.quota, WINDOW_1W_SECONDS, nowMs);

  return {
    name: group.label || (group.provider === 'codex' ? 'Codex / GPT' : '反重力 · Gemini'),
    badge,
    requestsTotal,
    requestsOk,
    requestsErr,
    successRate,
    cumulativeTokens,
    cacheHitRate,
    fiveHour,
    weekly,
  };
}

/**
 * Adapt full SummaryResponse into the two-column panel state.
 * Exact group matching:
 *   Left column: provider === 'codex' && pool === 'codex'
 *   Right column: provider === 'antigravity' && pool === 'antigravity-gemini-models'
 * When a group is missing in summary.groups, its column model is null (clearing old state).
 */
export function adaptSummaryResponse(summary, nowMs = Date.now()) {
  if (!summary || !Array.isArray(summary.groups)) {
    return {
      codex: null,
      antigravity: null,
      generatedAt: null,
      eligible: 0,
      pending: 0,
    };
  }

  const codexGroup = summary.groups.find(
    (g) => g.provider === 'codex' && g.pool === 'codex'
  );
  const agGroup = summary.groups.find(
    (g) => g.provider === 'antigravity' && g.pool === 'antigravity-gemini-models'
  );

  return {
    codex: adaptSummaryGroup(codexGroup, nowMs),
    antigravity: adaptSummaryGroup(agGroup, nowMs),
    generatedAt: summary.generated_at || null,
    eligible: summary.eligible ?? 0,
    pending: summary.pending ?? 0,
  };
}
