import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import WalletBalanceCard from '../WalletBalanceCard.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string>) =>
      ({
        'userSubscriptions.wallet.title': 'Wallet balance',
        'userSubscriptions.wallet.subtitle': 'Shared wallet quota',
        'userSubscriptions.wallet.remaining': 'Remaining',
        'userSubscriptions.wallet.usedPercent': 'Used',
        'userSubscriptions.wallet.lowWarning': `Only ${params?.amount} left`,
        'userSubscriptions.wallet.exhausted': 'Wallet exhausted',
        'userSubscriptions.status.active': 'Active',
        'userSubscriptions.expires': 'Expires',
        'payment.renewNow': 'Renew'
      })[key] || key
  })
}))

function mountWalletCard(balance = 399.4) {
  return mount(WalletBalanceCard, {
    props: {
      subscription: {
        id: 146,
        status: 'active',
        wallet_balance_usd: balance,
        wallet_initial_usd: 400,
        expires_at: '2026-07-04T00:00:00Z'
      }
    },
    global: {
      stubs: {
        Icon: {
          props: ['name', 'size'],
          template: '<span />'
        }
      }
    }
  })
}

describe('WalletBalanceCard', () => {
  it('keeps the renewal entry visible even when the wallet balance is not low', async () => {
    const wrapper = mountWalletCard(399.4)
    const renewButton = wrapper.get('[data-hfc-renew-entry="wallet"]')

    expect(renewButton.text()).toBe('Renew')
    expect(wrapper.text()).not.toContain('Only 399.40 left')

    await renewButton.trigger('click')

    expect(wrapper.emitted('renew')).toHaveLength(1)
  })

  it('shows a low-balance reminder without hiding the renewal entry', () => {
    const wrapper = mountWalletCard(100)

    expect(wrapper.get('[data-hfc-renew-entry="wallet"]').text()).toBe('Renew')
    expect(wrapper.text()).toContain('Only 100.00 left')
  })
})
