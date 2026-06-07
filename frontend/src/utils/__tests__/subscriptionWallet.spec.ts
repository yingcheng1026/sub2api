import { describe, expect, it } from 'vitest'
import {
  ANTI_OVERWRITE_WALLET_MARKER,
  getWalletInitialUSD,
  getWalletRemainingUSD,
  getWalletUsedPercent,
  hasWalletBalance
} from '../subscriptionWallet'

describe('subscription wallet helpers', () => {
  it('treats a zero wallet balance as wallet mode', () => {
    expect(hasWalletBalance({ wallet_balance_usd: 0, wallet_initial_usd: 1500 })).toBe(true)
  })

  it('does not treat legacy group subscriptions as wallet mode', () => {
    expect(hasWalletBalance({ wallet_balance_usd: null, wallet_initial_usd: null })).toBe(false)
    expect(hasWalletBalance({})).toBe(false)
  })

  it('normalizes wallet amounts and usage percentage', () => {
    const subscription = { wallet_balance_usd: 375, wallet_initial_usd: 1500 }

    expect(getWalletRemainingUSD(subscription)).toBe(375)
    expect(getWalletInitialUSD(subscription)).toBe(1500)
    expect(getWalletUsedPercent(subscription)).toBe(75)
  })

  it('keeps the anti-overwrite marker stable for image scans', () => {
    expect(ANTI_OVERWRITE_WALLET_MARKER).toBe('hasWalletBalance')
  })
})

