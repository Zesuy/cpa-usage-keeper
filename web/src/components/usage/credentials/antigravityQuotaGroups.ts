import type { AntigravityQuotaGroup } from '@/stores/useAntigravityQuotaDisplayStore'
import type { DisplayQuota } from './credentialViewModels'

export const ANTIGRAVITY_DISPLAY_GROUP_KEYS: Record<AntigravityQuotaGroup, string> = {
  gemini: 'antigravity-gemini-models',
  claudeGpt: 'antigravity-claude-and-gpt-models',
  argon: 'antigravity-gemini-4-argon',
}

export function antigravityQuotaGroup(quota: Pick<DisplayQuota, 'scope' | 'groupKey' | 'groupLabel'>): AntigravityQuotaGroup | undefined {
  const groupKey = quota.groupKey?.trim() ?? ''
  if (quota.scope !== 'quota_group' || !groupKey.startsWith('antigravity-')) return undefined

  // Upstream can give Argon the same key as Gemini Models; the title distinguishes them.
  const label = quota.groupLabel?.trim().toLowerCase() ?? ''
  if (/\bgemini\b/.test(label) && /\bargon\b/.test(label)) return 'argon'
  if (groupKey === ANTIGRAVITY_DISPLAY_GROUP_KEYS.argon) return 'argon'
  if (groupKey === ANTIGRAVITY_DISPLAY_GROUP_KEYS.gemini) return 'gemini'
  if (groupKey === ANTIGRAVITY_DISPLAY_GROUP_KEYS.claudeGpt) return 'claudeGpt'
  return undefined
}
