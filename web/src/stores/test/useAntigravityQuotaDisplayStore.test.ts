// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ANTIGRAVITY_QUOTA_DISPLAY_STORAGE_KEY, defaultAntigravityQuotaDisplayPreferences, loadAntigravityQuotaDisplayPreferences, useAntigravityQuotaDisplayStore } from '../useAntigravityQuotaDisplayStore'

beforeEach(() => {
  localStorage.clear()
  useAntigravityQuotaDisplayStore.setState({ groups: defaultAntigravityQuotaDisplayPreferences() })
})

afterEach(() => vi.restoreAllMocks())

describe('Antigravity quota display preferences', () => {
  it('defaults to all groups visible and restores explicit false values', () => {
    expect(loadAntigravityQuotaDisplayPreferences()).toEqual({ gemini: true, claudeGpt: true, argon: true })
    const { setGroupVisible } = useAntigravityQuotaDisplayStore.getState()
    setGroupVisible('gemini', false)
    setGroupVisible('claudeGpt', false)
    setGroupVisible('argon', false)
    expect(loadAntigravityQuotaDisplayPreferences()).toEqual({ gemini: false, claudeGpt: false, argon: false })
    setGroupVisible('argon', true)
    expect(loadAntigravityQuotaDisplayPreferences()).toEqual({ gemini: false, claudeGpt: false, argon: true })
  })

  it.each(['{', 'null', '[]', '{"version":2,"groups":{"gemini":false}}', '{"version":1,"groups":{"gemini":"false","argon":0}}'])(
    'uses defaults for invalid stored preferences: %s', (raw) => {
      localStorage.setItem(ANTIGRAVITY_QUOTA_DISPLAY_STORAGE_KEY, raw)
      expect(loadAntigravityQuotaDisplayPreferences()).toEqual(defaultAntigravityQuotaDisplayPreferences())
    },
  )

  it('uses defaults for missing fields without overwriting valid false values', () => {
    localStorage.setItem(ANTIGRAVITY_QUOTA_DISPLAY_STORAGE_KEY, JSON.stringify({ version: 1, groups: { gemini: false } }))
    expect(loadAntigravityQuotaDisplayPreferences()).toEqual({ gemini: false, claudeGpt: true, argon: true })
  })

  it('keeps in-memory choices when storage reads or writes are blocked', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    expect(loadAntigravityQuotaDisplayPreferences()).toEqual(defaultAntigravityQuotaDisplayPreferences())
    expect(() => useAntigravityQuotaDisplayStore.getState().setGroupVisible('gemini', false)).not.toThrow()
    expect(useAntigravityQuotaDisplayStore.getState().groups.gemini).toBe(false)
  })
})
