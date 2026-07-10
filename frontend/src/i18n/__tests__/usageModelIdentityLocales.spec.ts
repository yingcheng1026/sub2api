import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

const identityKeys = [
  'executedModel',
  'billingModel',
  'compatibilityMode',
  'compatNativeGPT',
  'compatLegacyClaudeAlias',
  'compatOtherHistorical',
] as const

describe('usage model identity locales', () => {
  it.each([
    ['English', en],
    ['Chinese', zh],
  ])('defines all model identity labels in %s', (_name, locale) => {
    for (const key of identityKeys) {
      expect(locale.usage[key]).toEqual(expect.any(String))
      expect(locale.usage[key].trim()).not.toBe('')
    }
  })
})
