import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Card } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { ApiError, fetchQuotaSummary, refreshQuotaSummary } from '@/lib/api'
import type { QuotaSummaryResponse } from '@/lib/types'
import { useAntigravityQuotaDisplayStore } from '@/stores/useAntigravityQuotaDisplayStore'
import { formatQuotaResetLabel, QuotaBar, QuotaUsageModeSwitch } from './CredentialsSection'
import { antigravityQuotaGroup } from './antigravityQuotaGroups'
import { toDisplayQuota } from './credentialViewModels'
import styles from './QuotaSummaryCard.module.scss'
import quotaStyles from './CredentialSections.module.scss'

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
      {loading && <p>{t('common.loading')}</p>}
      {!loading && !error && visibleGroups.length === 0 && <p className={styles.note}>{t(data?.eligible ? 'usage_stats.antigravity_quota_display_hidden' : 'usage_stats.quota_summary_empty')}</p>}
      {visibleGroups.map((group) => <section key={group.pool} className={styles.group}>
        <h4>{group.label}</h4>
        <div className={quotaStyles.credentialQuotaGroupBars}>
          {group.quota.map((row) => {
            const display = toDisplayQuota(row)
            const coverage = row.coverage
            if (!display || !coverage) return null
            const estimated = coverage.direct + coverage.historical + coverage.peer
            const covered = mode === 'current' ? coverage.current : estimated
            const details = t('usage_stats.quota_summary_details', { ...coverage })
            return <div key={row.key} className={styles.window}>
              {coverage.unsupported === coverage.eligible
                ? <p className={styles.note}>{display.label}: {t('usage_stats.quota_summary_unsupported')}</p>
                : <>
                  <QuotaBar quota={display} quotaUsageMode={mode} timeZone={timeZone} showGroupMetadata={false} />
                  {(mode === 'estimated' ? !display.windowUsageEstimate : !display.windowUsage) && <p className={styles.note}>{t(mode === 'estimated' ? 'usage_stats.quota_summary_unknown' : 'usage_stats.quota_summary_current_unknown')}</p>}
                  <p className={styles.coverage} title={details}>
                    {t('usage_stats.quota_summary_coverage', { covered, total: coverage.eligible })}
                    {mode === 'estimated' && coverage.historical + coverage.peer > 0 && <> · {t('usage_stats.quota_summary_filled', { count: coverage.historical + coverage.peer })}</>}
                    {mode === 'estimated' && coverage.oldest_estimate_at && <span className={styles.historyDate}>{t('usage_stats.quota_summary_estimate_date', { date: formatQuotaResetLabel(coverage.oldest_estimate_at, timeZone) || new Date(coverage.oldest_estimate_at).toLocaleString() })}</span>}
                  </p>
                  <details className={styles.details}><summary>{t('usage_stats.quota_summary_sources')}</summary>{details}</details>
                </>}
            </div>
          })}
        </div>
      </section>)}
    </Card>
  )
}
