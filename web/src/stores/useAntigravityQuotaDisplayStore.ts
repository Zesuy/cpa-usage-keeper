import { create } from 'zustand'

export interface AntigravityQuotaDisplayPreferences {
  gemini: boolean
  claudeGpt: boolean
  argon: boolean
}

export type AntigravityQuotaGroup = keyof AntigravityQuotaDisplayPreferences

export const ANTIGRAVITY_QUOTA_DISPLAY_STORAGE_KEY = 'cpa.antigravity.quotaDisplay.v1'

export const defaultAntigravityQuotaDisplayPreferences = (): AntigravityQuotaDisplayPreferences => ({
  gemini: true,
  claudeGpt: true,
  argon: true,
})

export function loadAntigravityQuotaDisplayPreferences(): AntigravityQuotaDisplayPreferences {
  const defaults = defaultAntigravityQuotaDisplayPreferences()
  try {
    const raw = window.localStorage.getItem(ANTIGRAVITY_QUOTA_DISPLAY_STORAGE_KEY)
    if (!raw) return defaults
    const stored = JSON.parse(raw)
    if (stored?.version !== 1 || !stored.groups || typeof stored.groups !== 'object') return defaults
    return {
      gemini: typeof stored.groups.gemini === 'boolean' ? stored.groups.gemini : true,
      claudeGpt: typeof stored.groups.claudeGpt === 'boolean' ? stored.groups.claudeGpt : true,
      argon: typeof stored.groups.argon === 'boolean' ? stored.groups.argon : true,
    }
  } catch {
    // Missing, blocked or corrupt storage must not prevent quota rendering.
    return defaults
  }
}

interface AntigravityQuotaDisplayState {
  groups: AntigravityQuotaDisplayPreferences
  setGroupVisible: (group: AntigravityQuotaGroup, visible: boolean) => void
}

export const useAntigravityQuotaDisplayStore = create<AntigravityQuotaDisplayState>((set, get) => ({
  groups: loadAntigravityQuotaDisplayPreferences(),
  setGroupVisible: (group, visible) => {
    const groups = { ...get().groups, [group]: visible }
    set({ groups })
    try {
      window.localStorage.setItem(ANTIGRAVITY_QUOTA_DISPLAY_STORAGE_KEY, JSON.stringify({ version: 1, groups }))
    } catch {
      // Keep the current page's selection even when browser storage is unavailable.
    }
  },
}))
