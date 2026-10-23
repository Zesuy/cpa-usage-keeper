import React, { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Card } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { ProviderBrandIcon } from '@/components/ProviderBrandIcon'
import { IconInfo } from '@/components/ui/icons'
import { ApiError, fetchQuotaSummary, refreshQuotaSummary } from '@/lib/api'
import type { QuotaSummaryResponse, UsageQuotaRow } from '@/lib/types'
import { useAntigravityQuotaDisplayStore } from '@/stores/useAntigravityQuotaDisplayStore'
import { formatQuotaResetLabel, QuotaBar, QuotaUsageModeSwitch } from './CredentialsSection'
import { antigravityQuotaGroup } from './antigravityQuotaGroups'
import { toDisplayQuota } from './credentialViewModels'
import styles from './QuotaSummaryCard.module.scss'

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
  return (
    <article className={styles.summaryRow}>
      <div className={styles.identityBlock}>
        <div className={styles.identityIconSlot}>
          <ProviderBrandIcon providerType={group.provider} size={20} ariaLabel={group.label} />
        </div>
        <div className={styles.identityContent}>
          <span className={styles.groupTitle} title={group.label}>{group.label}</span>
        </div>
      </div>

      <div className={styles.quotaSide}>
        {group.quota.map((row) => (
          <QuotaSummaryWindowBar
            key={row.key}
            row={row}
            mode={mode}
            timeZone={timeZone}
          />
        ))}
      </div>
    </article>
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
  const details = t('usage_stats.quota_summary_details', { ...coverage })
  const filledCount = coverage.historical + coverage.peer
  const oldestEstimateAt = coverage.oldest_estimate_at
  const isUnknown = mode === 'estimated' ? !display.windowUsageEstimate : !display.windowUsage

  const headerExtra = (
    <span className={styles.windowHeaderExtra}>
      <span className={styles.coverageTag} title={details}>
        {t('usage_stats.quota_summary_coverage', { covered, total: coverage.eligible })}
      </span>
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
      />
      {isUnknown && (
        <div className={styles.windowMetaNote}>
          {t(mode === 'estimated' ? 'usage_stats.quota_summary_unknown' : 'usage_stats.quota_summary_current_unknown')}
        </div>
      )}
    </div>
  )
}
