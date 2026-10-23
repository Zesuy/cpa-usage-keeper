// @vitest-environment happy-dom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defaultAntigravityQuotaDisplayPreferences, loadAntigravityQuotaDisplayPreferences, useAntigravityQuotaDisplayStore, type AntigravityQuotaDisplayPreferences } from '@/stores/useAntigravityQuotaDisplayStore'
import { AntigravityQuotaDisplaySettingsCard } from '../../AntigravityQuotaDisplaySettingsCard'
import { CredentialQuotaPanel, CredentialsSection } from '../CredentialsSection'
import { CredentialDetailDrawer } from '../CredentialDetailDrawer'
import { buildCredentialRows, type CredentialRow, type DisplayQuota } from '../credentialViewModels'
import { antigravityQuotaGroup } from '../antigravityQuotaGroups'
import { createCredentialSectionProps } from './credentialSectionFixtures'
import type { UsageIdentity } from '@/lib/types'

globalThis.IS_REACT_ACT_ENVIRONMENT = true

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({ t: (key: string) => key }),
}))

const row = buildCredentialRows([{
  id: 'antigravity-1', identity: 'auth-antigravity', type: 'antigravity', auth_type: 1, is_deleted: false,
} as UsageIdentity])[0]

const quota = (groupLabel: string, bucket: string, groupKey = 'antigravity-gemini-models'): DisplayQuota => ({
  key: `bucket.${groupKey}.${bucket}`, label: bucket, scope: 'quota_group', groupKey, groupLabel,
  groupDescription: `${groupLabel} description`, percent: 95, barPercent: 95, percentKind: 'remaining', status: 'ok',
  windowUsage: { tokens: `${bucket}-tokens`, cost: `${bucket}-cost` },
})

// Argon deliberately shares the server's Gemini key, matching current upstream responses.
const groupedRow: CredentialRow = { ...row, displayQuotas: [
  quota('Gemini Models', 'gemini-5h'),
  quota('Gemini Models', 'gemini-weekly'),
  quota('Claude and GPT models', 'claude-weekly', 'antigravity-claude-and-gpt-models'),
  quota('Gemini 4 Argon', 'argon-weekly'),
] }

describe('Antigravity quota display', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    localStorage.clear()
    useAntigravityQuotaDisplayStore.setState({ groups: defaultAntigravityQuotaDisplayPreferences() })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
    vi.restoreAllMocks()
  })

  const preferences = Array.from({ length: 8 }, (_, mask) => ({
    gemini: Boolean(mask & 1), claudeGpt: Boolean(mask & 2), argon: Boolean(mask & 4),
  }))

  it.each(preferences)('renders only enabled groups for %j', async (groups: AntigravityQuotaDisplayPreferences) => {
    useAntigravityQuotaDisplayStore.setState({ groups })
    await act(async () => root.render(<CredentialQuotaPanel row={groupedRow} quotaUsageMode="current" />))
    expect(container.querySelectorAll('[data-quota-group]')).toHaveLength(Object.values(groups).filter(Boolean).length)
    for (const [group, label, bucket] of [
      ['gemini', 'Gemini Models', 'gemini-5h'],
      ['claudeGpt', 'Claude and GPT models', 'claude-weekly'],
      ['argon', 'Gemini 4 Argon', 'argon-weekly'],
    ] as const) {
      const text = container.textContent ?? ''
      expect(text.includes(label)).toBe(groups[group])
      expect(text.includes(`${bucket}-tokens`)).toBe(groups[group])
      expect(text.includes(`${bucket}-cost`)).toBe(groups[group])
      expect(container.innerHTML.includes(`${label} description`)).toBe(groups[group])
    }
    expect(container.textContent?.includes('antigravity_quota_display_hidden')).toBe(!Object.values(groups).some(Boolean))
    expect(container.textContent).not.toContain('credentials_quota_unavailable')
    // Filtering must not modify the original quota rows or their server keys.
    expect(groupedRow.displayQuotas).toHaveLength(4)
    expect(groupedRow.displayQuotas[3].groupKey).toBe('antigravity-gemini-models')
  })

  it('updates both the real credential list and open detail drawer from settings, then restores the choices', async () => {
    await act(async () => root.render(<>
      <AntigravityQuotaDisplaySettingsCard />
      <CredentialsSection {...createCredentialSectionProps({ rows: [groupedRow], total: 1 })} />
      <CredentialDetailDrawer open selection={{ kind: 'credential', row: groupedRow }} onClose={() => undefined} />
    </>))
    expect(document.querySelectorAll('[data-quota-group="antigravity-gemini-4-argon"]')).toHaveLength(2)
    const inputs = container.querySelectorAll<HTMLInputElement>('.card input[type="checkbox"]')
    expect(inputs).toHaveLength(3)
    expect([...inputs].every((input) => input.checked && input.labels?.length === 1)).toBe(true)
    await act(async () => {
      inputs[0].click()
      inputs[1].click()
    })
    expect(document.querySelectorAll('[data-quota-group="antigravity-gemini-models"]')).toHaveLength(0)
    expect(document.querySelectorAll('[data-quota-group="antigravity-claude-and-gpt-models"]')).toHaveLength(0)
    expect(document.querySelectorAll('[data-quota-group="antigravity-gemini-4-argon"]')).toHaveLength(2)
    expect(loadAntigravityQuotaDisplayPreferences()).toEqual({ gemini: false, claudeGpt: false, argon: true })
    await act(async () => root.render(<AntigravityQuotaDisplaySettingsCard />))
    await act(async () => useAntigravityQuotaDisplayStore.setState({ groups: loadAntigravityQuotaDisplayPreferences() }))
    expect([...container.querySelectorAll<HTMLInputElement>('input')].map((input) => input.checked)).toEqual([false, false, true])
  })

  it('leaves unknown groups and other providers visible when all three groups are off', async () => {
    useAntigravityQuotaDisplayStore.setState({ groups: { gemini: false, claudeGpt: false, argon: false } })
    const others = [
      quota('Future models', 'future', 'antigravity-future-models'),
      { ...quota('Codex', 'codex'), scope: 'window', groupKey: undefined },
      quota('Gemini 4 Argon', 'other-provider', 'other-provider-group'),
    ]
    await act(async () => root.render(<CredentialQuotaPanel row={{ ...groupedRow, displayQuotas: [...groupedRow.displayQuotas, ...others] }} quotaUsageMode="current" />))
    for (const bucket of ['future', 'codex', 'other-provider']) expect(container.textContent).toContain(`${bucket}-tokens`)
    expect(container.textContent).not.toContain('antigravity_quota_display_hidden')
    expect(container.textContent).not.toContain('gemini-5h-tokens')
  })

  it.each([
    [{ quotaLoading: true }, 'credentials_quota_loading'],
    [{ quotaError: 'HTTP 401: expired' }, 'expired'],
    [{ refreshStatus: 'running' }, 'credentials_refresh_status_running'],
    [{ displayQuotas: [] }, 'credentials_quota_unavailable'],
  ] as const)('retains status precedence for %j', async (patch, message) => {
    useAntigravityQuotaDisplayStore.setState({ groups: { gemini: false, claudeGpt: false, argon: false } })
    await act(async () => root.render(<CredentialQuotaPanel row={{ ...groupedRow, ...patch }} quotaUsageMode="current" />))
    expect(container.textContent).toContain(message)
    expect(container.textContent).not.toContain('antigravity_quota_display_hidden')
  })

  it('recognizes Argon before generic Gemini across title case and key variations', () => {
    expect(antigravityQuotaGroup(quota(' GEMINI 4 ARGON ', 'argon'))).toBe('argon')
    expect(antigravityQuotaGroup(quota('Gemini 4 Argon', 'argon', 'antigravity-gemini-4-argon'))).toBe('argon')
    expect(antigravityQuotaGroup(quota('Gemini Models', 'gemini'))).toBe('gemini')
  })
})
