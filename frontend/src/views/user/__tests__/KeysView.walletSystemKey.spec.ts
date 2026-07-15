import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { WALLET_UNIVERSAL_KEY_NAME } from '@/utils/walletKeys'

const listKeys = vi.hoisted(() => vi.fn())
const toggleStatus = vi.hoisted(() => vi.fn())
const deleteKey = vi.hoisted(() => vi.fn())
const revealKey = vi.hoisted(() => vi.fn())
const getUsage = vi.hoisted(() => vi.fn())
const getAvailableGroups = vi.hoisted(() => vi.fn())
const getUserGroupRates = vi.hoisted(() => vi.fn())
const getPublicSettings = vi.hoisted(() => vi.fn())
const getActiveSubscriptions = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() }),
}))
vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() }),
}))
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() }),
}))
vi.mock('@/composables/usePersistedPageSize', () => ({ getPersistedPageSize: () => 20 }))
vi.mock('@/api', () => ({
  keysAPI: {
    list: listKeys,
    toggleStatus,
    delete: deleteKey,
    reveal: revealKey,
  },
  authAPI: { getPublicSettings },
  usageAPI: { getDashboardApiKeysUsage: getUsage },
  userGroupsAPI: {
    getAvailable: getAvailableGroups,
    getUserGroupRates,
  },
}))
vi.mock('@/api/subscriptions', () => ({
  default: { getActiveSubscriptions },
}))

import KeysView from '../KeysView.vue'

const systemWalletKey = {
  id: 42,
  user_id: 9,
  name: WALLET_UNIVERSAL_KEY_NAME,
  purpose: 'wallet_universal',
  key: 'sk-wall••••',
  group_id: null,
  group: null,
  status: 'active',
  quota: 0,
  quota_used: 0,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  ip_whitelist: [],
  ip_blacklist: [],
  expires_at: null,
  created_at: '2026-07-12T00:00:00Z',
  updated_at: '2026-07-12T00:00:00Z',
  last_used_at: null,
}

describe('KeysView system wallet key controls', () => {
  beforeEach(() => {
    listKeys.mockReset().mockResolvedValue({ items: [systemWalletKey], total: 1, pages: 1 })
    toggleStatus.mockReset().mockResolvedValue(undefined)
    deleteKey.mockReset().mockResolvedValue(undefined)
    revealKey.mockReset().mockResolvedValue('sk-wallet-system')
    getUsage.mockReset().mockResolvedValue({ stats: {} })
    getAvailableGroups.mockReset().mockResolvedValue([])
    getUserGroupRates.mockReset().mockResolvedValue({})
    getPublicSettings.mockReset().mockResolvedValue({ hide_ccs_import_button: true })
    getActiveSubscriptions.mockReset().mockResolvedValue([{
      id: 7,
      status: 'active',
      group_id: null,
      wallet_balance_usd: 50,
      expires_at: '2099-12-31T23:59:59Z',
    }])
    showError.mockReset()
  })

  it('hides edit, group, and delete mutation while preserving disable control', async () => {
    const wrapper = shallowMount(KeysView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: {
            props: ['data'],
            template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-group" :row="row" /><slot name="cell-actions" :row="row" /></div></div>',
          },
          Icon: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).not.toContain('common.edit')
    expect(wrapper.text()).toContain('keys.disable')
    expect(wrapper.text()).not.toContain('common.delete')
    expect(wrapper.find('[title="keys.walletAnyKeyHint"]').exists()).toBe(true)
  })

  it('never exposes a raw API key through the retired CCS deep-link import', async () => {
    const openWindow = vi.fn()
    Object.defineProperty(window, 'open', { configurable: true, writable: true, value: openWindow })
    getPublicSettings.mockResolvedValue({ hide_ccs_import_button: false, api_base_url: 'https://relay.example.com' })

    const wrapper = shallowMount(KeysView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="table" /></div>' },
          DataTable: {
            props: ['data'],
            template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>',
          },
          Icon: true,
          Teleport: true,
          Transition: false,
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>',
          },
        },
      },
    })
    await flushPromises()

    const importButton = wrapper.findAll('button').find((button) => button.text().includes('keys.importToCcSwitch'))
    expect(importButton).toBeUndefined()
    expect(revealKey).not.toHaveBeenCalled()
    expect(openWindow).not.toHaveBeenCalled()
  })

  it('drops late raw-key responses after the reveal generation is invalidated', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/views/user/KeysView.vue'), 'utf8')

    expect(source).toMatch(
      /const requestGeneration = revealRequestGeneration\s+const plaintext = await keysAPI\.reveal\(keyId, verification\)\s+if \(requestGeneration !== revealRequestGeneration \|\| document\.hidden\) return\s+await action\(plaintext\)/
    )
    expect(source).toMatch(
      /const createdKey = await keysAPI\.create\([\s\S]*?\)\s+if \(requestGeneration !== revealRequestGeneration \|\| document\.hidden\) \{\s+createdKey\.key = ''/
    )
  })

  it('routes API-key authority expansion through fresh verification', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/views/user/KeysView.vue'), 'utf8')

    expect(source).toMatch(
      /if \(newStatus === 'active'\) \{\s+requestKeyUpdateStepUp\([\s\S]*?\{ status: newStatus \}/
    )
    expect(source).toMatch(
      /requestKeyUpdateStepUp\(\s+key\.id,\s+\{ group_id: newGroupId \}/
    )
    expect(source).toMatch(
      /requestKeyUpdateStepUp\(\s+keyId,\s+\{ reset_quota: true \}/
    )
    expect(source).toMatch(
      /requestKeyUpdateStepUp\(\s+keyId,\s+\{ reset_rate_limit_usage: true \}/
    )
    expect(source).toMatch(
      /await keysAPI\.update\(keyId, \{\s+verification,\s+name:/
    )
  })
})
