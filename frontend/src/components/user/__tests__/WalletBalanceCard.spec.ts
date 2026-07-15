import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import WalletBalanceCard from '../WalletBalanceCard.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) =>
        ({
          'userSubscriptions.wallet.title': 'Wallet balance',
          'userSubscriptions.wallet.subtitle': 'Shared wallet quota',
          'userSubscriptions.wallet.remaining': 'Remaining',
          'userSubscriptions.wallet.usedPercent': 'Used',
          'userSubscriptions.wallet.lowWarning': `Only ${params?.amount} left`,
          'userSubscriptions.wallet.exhausted': 'Wallet exhausted',
          'userSubscriptions.wallet.debtToCover': 'Debt to cover',
          'userSubscriptions.wallet.debtWarning': `Outstanding debt $${params?.amount}`,
          'userSubscriptions.status.active': 'Active',
          'userSubscriptions.expires': 'Expires',
          'payment.renewNow': 'Renew'
        })[key] || key
    })
  }
})

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

  it('shows negative balance as debt and clamps progress to 100 percent', () => {
    const wrapper = mountWalletCard(-25.5)

    expect(wrapper.text()).toContain('Debt to cover')
    expect(wrapper.text()).toContain('Outstanding debt $25.50')
    expect(wrapper.text()).not.toContain('Wallet exhausted')
    expect(wrapper.get('[data-hfc-wallet-progress]').attributes('style')).toContain('width: 100%')
  })
})
