import { useTranslation } from 'react-i18next'
import { Card } from '@/components/ui/Card'
import { useAntigravityQuotaDisplayStore, type AntigravityQuotaGroup } from '@/stores/useAntigravityQuotaDisplayStore'
import styles from './AntigravityQuotaDisplaySettingsCard.module.scss'
import quotaStyles from './credentials/CredentialSections.module.scss'

const GROUPS: AntigravityQuotaGroup[] = ['gemini', 'claudeGpt', 'argon']

export function AntigravityQuotaDisplaySettingsCard() {
  const { t } = useTranslation()
  const groups = useAntigravityQuotaDisplayStore((state) => state.groups)
  const setGroupVisible = useAntigravityQuotaDisplayStore((state) => state.setGroupVisible)

  return (
    <Card
      className={styles.card}
      title={t('usage_stats.antigravity_quota_display_title')}
      subtitle={t('usage_stats.antigravity_quota_display_description')}
    >
      <div className={styles.switches}>
        {GROUPS.map((group) => (
          <label key={group} className={quotaStyles.credentialActiveOnlySwitch}>
            <span className={quotaStyles.credentialActiveOnlyLabel}>{t(`usage_stats.antigravity_quota_display_${group}`)}</span>
            <input type="checkbox" checked={groups[group]} onChange={(event) => setGroupVisible(group, event.target.checked)} />
            <span className={quotaStyles.credentialActiveOnlyTrack} aria-hidden="true">
              <span className={quotaStyles.credentialActiveOnlyThumb} />
            </span>
          </label>
        ))}
      </div>
    </Card>
  )
}
