import React, { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Card } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { ProviderBrandIcon } from '@/components/ProviderBrandIcon'
import { IconInfo } from '@/components/ui/icons'
import { ApiError, fetchQuotaSummary, refreshQuotaSummary } from '@/lib/api'
import type { QuotaSummaryResponse, UsageIdentityPeriodStats, UsageQuotaRow } from '@/lib/types'
import { calculateCacheReadRate } from '@/utils/usage'
import { useAntigravityQuotaDisplayStore } from '@/stores/useAntigravityQuotaDisplayStore'
import {
  formatQuotaResetLabel,
  QuotaBar,
  QuotaUsageModeSwitch,
} from './CredentialsSection'
import {
  CredentialRowShell,
  CredentialTableHeader,
  MetricPill,
  RequestMetric,
  TonePercent,
  cacheReadRateTone,
  formatCredentialNumber,
  successRateTone,
} from './CredentialSectionShell'
import { antigravityQuotaGroup } from './antigravityQuotaGroups'
import { toDisplayQuota } from './credentialViewModels'
import styles from './QuotaSummaryCard.module.scss'
import quotaStyles from './CredentialSections.module.scss'

type QuotaSummaryGroup = QuotaSummaryResponse['groups'][number]

export function QuotaSummaryCard({ timeZone, onAuthRequired }: { timeZone?: string; onAuthRequired?: () => void }) {
  const { t } = useTranslation()
  const [data, setData] = useState<QuotaSummaryResponse | null>(null)
  const [mode, setMode] = useState<'current' | 'estimated'>('current')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  const mounted = useRef(false)
  const groups = useAntigravityQuotaDisplayStore((state) => state.groups)

  const load = useCallback(async () => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    try {
      const response = await fetchQuotaSummary(request.signal)
      if (request.signal.aborted) return
      setData(response)
      setError('')
    } catch (nextError) {
      if (request.signal.aborted) return
      if (nextError instanceof ApiError && nextError.status === 401) onAuthRequired?.()
      setError(nextError instanceof Error ? nextError.message : 'usage_stats.quota_summary_load_failed')
    } finally {
      if (!request.signal.aborted) setLoading(false)
    }
  }, [onAuthRequired])

  const interval = data?.pending ? 5_000 : 60_000
  useEffect(() => {
    mounted.current = true
    void load()
    const timer = window.setInterval(() => { void load() }, interval)
    return () => {
      mounted.current = false
      window.clearInterval(timer)
      controller.current?.abort()
    }
  }, [interval, load])

  const refresh = async () => {
    if (submitting || data?.pending) return
    setSubmitting(true)
    setError('')
    try {
      await refreshQuotaSummary()
      if (mounted.current) await load()
    } catch (nextError) {
      if (!mounted.current) return
      if (nextError instanceof ApiError && nextError.status === 401) onAuthRequired?.()
      setError(nextError instanceof Error ? nextError.message : 'usage_stats.quota_summary_refresh_failed')
    } finally {
      if (mounted.current) setSubmitting(false)
    }
  }

  const visibleGroups = data?.groups.filter((group) => {
    if (group.provider !== 'antigravity') return true
    const kind = antigravityQuotaGroup({ scope: 'quota_group', groupKey: group.pool, groupLabel: group.label })
    return !kind || groups[kind]
  }) ?? []

  return (
    <Card
      variant="flush"
      className={styles.card}
      title={t('usage_stats.quota_summary_title')}
      subtitle={t('usage_stats.quota_summary_description')}
      extra={<div className={styles.controls}>
        <QuotaUsageModeSwitch label={t('usage_stats.credentials_quota_usage_mode_label')} mode={mode} onChange={setMode} />
        <Button disabled={loading || submitting || Boolean(data?.pending)} onClick={() => { void refresh() }}>
          {data?.pending ? t('usage_stats.quota_summary_pending', { count: data.pending }) : t('usage_stats.quota_summary_refresh_all')}
        </Button>
      </div>}
    >
      {error && <p role="alert" className={styles.error}>{t(error)}</p>}
      {loading && <p className={styles.emptyNote}>{t('common.loading')}</p>}
      {!loading && !error && visibleGroups.length === 0 && <p className={styles.emptyNote}>{t(data?.eligible ? 'usage_stats.antigravity_quota_display_hidden' : 'usage_stats.quota_summary_empty')}</p>}
      {!loading && visibleGroups.length > 0 && (
        <CredentialTableHeader
          rowClassName={quotaStyles.credentialsRow}
          nameLabel={t('usage_stats.credentials_column_name')}
          totalRequestsLabel={t('usage_stats.total_requests')}
          successRateLabel={t('usage_stats.success_rate')}
          totalTokensLabel={t('usage_stats.total_tokens')}
          cacheReadRateLabel={t('usage_stats.cache_rate')}
          sideLabel={t('usage_stats.credentials_column_quota')}
        />
      )}
      <div className={styles.rows}>
        {visibleGroups.map((group) => (
          <QuotaSummaryGroupRow
            key={group.pool}
            group={group}
            mode={mode}
            timeZone={timeZone}
          />
        ))}
      </div>
    </Card>
  )
}

function QuotaSummaryGroupRow({
  group,
  mode,
  timeZone,
}: {
  group: QuotaSummaryGroup
  mode: 'current' | 'estimated'
  timeZone?: string
}) {
  const { t } = useTranslation()
  const metricsTitle = t('usage_stats.quota_summary_metrics_scope')
  const stats = group.stats
  const totalRequests = stats?.total_requests ?? 0
  const successCount = stats?.success_count ?? 0
  const failureCount = stats?.failure_count ?? 0
  const successRate = totalRequests > 0 ? (successCount / totalRequests) * 100 : null
  const totalTokens = stats?.total_tokens ?? 0
  const cacheReadRate = stats
    ? calculateCacheReadRate({ inputTokens: stats.input_tokens, cacheReadTokens: stats.cache_read_tokens })
    : null

  const metrics = stats ? (
    <>
      <MetricPill value={<RequestMetric total={totalRequests} success={successCount} failure={failureCount} />} />
      <MetricPill value={<TonePercent value={successRate} tone={successRateTone(successRate)} />} />
      <MetricPill value={formatCredentialNumber(totalTokens)} />
      <MetricPill value={<TonePercent value={cacheReadRate} tone={cacheReadRateTone(cacheReadRate)} />} />
    </>
  ) : (
    <>
      <MetricPill value="—" />
      <MetricPill value="—" />
      <MetricPill value="—" />
      <MetricPill value="—" />
    </>
  )

  const side = (
    <div className={quotaStyles.credentialQuotaGroupBars}>
      {group.quota.map((row) => (
        <QuotaSummaryWindowBar
          key={row.key}
          row={row}
          mode={mode}
          timeZone={timeZone}
        />
      ))}
    </div>
  )

  return (
    <CredentialRowShell
      icon={(
        <div className={styles.groupIconSlot}>
          <ProviderBrandIcon providerType={group.provider} size={20} ariaLabel={group.label} />
        </div>
      )}
      title={<span className={styles.groupTitle} title={group.label}>{group.label}</span>}
      badges={null}
      metrics={metrics}
      metricsTitle={metricsTitle}
      side={side}
      rowClassName={quotaStyles.credentialsRow}
    />
  )
}

function QuotaSummaryWindowBar({
  row,
  mode,
  timeZone,
}: {
  row: UsageQuotaRow
  mode: 'current' | 'estimated'
  timeZone?: string
}) {
  const { t } = useTranslation()
  const display = toDisplayQuota(row)
  const coverage = row.coverage
  if (!display || !coverage) return null

  const isUnsupported = coverage.unsupported === coverage.eligible
  if (isUnsupported) {
    return (
      <div className={styles.windowItem}>
        <div className={styles.unsupportedBox}>
          {display.label}: {t('usage_stats.quota_summary_unsupported')}
        </div>
      </div>
    )
  }

  const estimated = coverage.direct + coverage.historical + coverage.peer
  const covered = mode === 'current' ? coverage.current : estimated
  const details = t('usage_stats.quota_summary_details', { retained: 0, expired: 0, ...coverage })
  const filledCount = coverage.historical + coverage.peer
  const oldestEstimateAt = coverage.oldest_estimate_at
  const isUnknown = mode === 'estimated' ? !display.windowUsageEstimate : !display.windowUsage
  const state = mode === 'estimated' ? row.estimate_state : row.usage_state
  const nextResetAt = coverage.next_reset_at
  const nextResetLabel = nextResetAt ? formatQuotaResetLabel(nextResetAt, timeZone) : ''
  const nextResetTitle = t('usage_stats.quota_summary_next_reset', { date: nextResetLabel })
  const snapshotAt = coverage.oldest_snapshot_at
  const snapshotLabel = snapshotAt ? formatQuotaResetLabel(snapshotAt, timeZone) || new Date(snapshotAt).toLocaleString() : ''
  const snapshotTitle = snapshotAt ? t('usage_stats.quota_summary_snapshot_date', { date: snapshotLabel }) : ''
  const unavailableKey = state && state !== 'current' && state !== 'cached'
    ? `usage_stats.quota_summary_state_${state}`
    : mode === 'estimated' ? 'usage_stats.quota_summary_unknown' : 'usage_stats.quota_summary_current_unknown'

  const headerExtra = (
    <span className={styles.windowHeaderExtra}>
      <span className={styles.coverageTag} title={details}>
        {t('usage_stats.quota_summary_coverage', { covered, total: coverage.eligible })}
      </span>
      {state === 'cached' && <span className={styles.coverageTag} title={snapshotTitle}>{t('usage_stats.quota_summary_cached')}</span>}
      {mode === 'estimated' && filledCount > 0 && (
        <span className={`${styles.coverageTag} ${styles.filledTag}`}>
          {t('usage_stats.quota_summary_filled', { count: filledCount })}
        </span>
      )}
      <details className={styles.sourcesDetails}>
        <summary tabIndex={0} role="button" aria-haspopup="true" aria-label={t('usage_stats.quota_summary_sources')}>
          <IconInfo size={12} />
        </summary>
        <div className={styles.sourcesPopover} role="tooltip">
          <div>{details}</div>
          {snapshotAt && <div className={styles.historyDateText}>{snapshotTitle}</div>}
          {mode === 'estimated' && oldestEstimateAt && (
            <div className={styles.historyDateText}>
              {t('usage_stats.quota_summary_estimate_date', {
                date: formatQuotaResetLabel(oldestEstimateAt, timeZone) || new Date(oldestEstimateAt).toLocaleString(),
              })}
            </div>
          )}
        </div>
      </details>
    </span>
  )

  return (
    <div className={styles.windowItem}>
      <QuotaBar
        quota={display}
        quotaUsageMode={mode}
        timeZone={timeZone}
        showGroupMetadata={false}
        headerExtra={headerExtra}
        metaExtra={nextResetLabel ? <time dateTime={nextResetAt} title={nextResetTitle} className={quotaStyles.credentialQuotaResetTime}>{nextResetLabel}</time> : undefined}
      />
      {isUnknown && (
        <div className={styles.windowMetaNote}>
          {t(unavailableKey)}
        </div>
      )}
    </div>
  )
}
