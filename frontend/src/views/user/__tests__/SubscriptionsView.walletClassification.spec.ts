import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'

const getMySubscriptions = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))
vi.mock('@/api/subscriptions', () => ({
  default: { getMySubscriptions },
}))

import SubscriptionsView from '../SubscriptionsView.vue'

const expiredWallet = {
  id: 1,
  status: 'expired',
  group_id: null,
  wallet_balance_usd: 80,
  wallet_initial_usd: 100,
  expires_at: '2099-12-31T23:59:59Z',
}

const activeWallet = {
  ...expiredWallet,
  id: 2,
  status: 'active',
}

const activeMonthly = {
  id: 3,
  status: 'active',
  group_id: 3,
  wallet_balance_usd: null,
  wallet_initial_usd: null,
  expires_at: '2026-08-01T00:00:00Z',
  group: {
    id: 3,
    name: 'openai-default monthly',
    platform: 'openai',
    subscription_type: 'subscription',
  },
}

const mountView = () => shallowMount(SubscriptionsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      WalletBalanceCard: {
        props: ['subscription'],
        template: '<div data-test="wallet-card">wallet-{{ subscription.id }}</div>',
      },
      GroupRateMultiplierList: true,
      RenewLiandongModal: true,
      Icon: true,
    },
  },
})

describe('SubscriptionsView wallet classification', () => {
  beforeEach(() => {
    getMySubscriptions.mockReset()
    showError.mockReset()
  })

  it('does not let an expired historical wallet hide an active monthly subscription', async () => {
    getMySubscriptions.mockResolvedValue([expiredWallet, activeMonthly])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="wallet-card"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('openai-default monthly')
  })

  it('renders an active permanent wallet and active monthly subscription together', async () => {
    getMySubscriptions.mockResolvedValue([activeWallet, activeMonthly])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="wallet-card"]').text()).toContain('wallet-2')
    expect(wrapper.text()).toContain('openai-default monthly')
  })
})
