import { describe, expect, it } from 'vitest'
import {
  ANTI_OVERWRITE_WALLET_MARKER,
  getActiveCreditsWalletBalanceUSD,
  getWalletInitialUSD,
  getWalletRemainingUSD,
  getWalletUsedPercent,
  hasWalletBalance,
  isActiveCreditsWallet
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

  it('clamps wallet usage percentage to the visible 0-100 range', () => {
    expect(getWalletUsedPercent({ wallet_balance_usd: -25, wallet_initial_usd: 100 })).toBe(100)
    expect(getWalletUsedPercent({ wallet_balance_usd: 125, wallet_initial_usd: 100 })).toBe(0)
  })

  it('keeps the anti-overwrite marker stable for image scans', () => {
    expect(ANTI_OVERWRITE_WALLET_MARKER).toBe('hasWalletBalance')
  })

  it('reads the balance only from the active permanent credits wallet', () => {
    const subscriptions = [
      {
        status: 'active',
        group_id: 3,
        wallet_balance_usd: 999,
        expires_at: '2026-08-01T00:00:00Z'
      },
      {
        status: 'revoked',
        group_id: null,
        wallet_balance_usd: 888,
        expires_at: '2099-12-31T23:59:59Z'
      },
      {
        status: 'active',
        group_id: null,
        wallet_balance_usd: 321.45,
        expires_at: '2099-12-31T23:59:59Z'
      }
    ]

    expect(getActiveCreditsWalletBalanceUSD(subscriptions)).toBe(321.45)
  })

  it('returns zero when no active permanent credits wallet exists', () => {
    expect(getActiveCreditsWalletBalanceUSD([])).toBe(0)
    expect(getActiveCreditsWalletBalanceUSD([
      {
        status: 'active',
        group_id: 3,
        wallet_balance_usd: 88,
        expires_at: '2026-08-01T00:00:00Z'
      }
    ])).toBe(0)
  })

  it('uses the same MaxExpiresAt minus 24 hours boundary as the backend', () => {
    expect(isActiveCreditsWallet({
      status: 'active',
      group_id: 3,
      wallet_balance_usd: 88,
      expires_at: '2099-12-31T23:59:59Z'
    })).toBe(false)
    expect(isActiveCreditsWallet({
      status: 'active',
      group_id: null,
      wallet_balance_usd: 88,
      expires_at: '2099-12-01T00:00:00Z'
    })).toBe(false)
    expect(isActiveCreditsWallet({
      status: 'active',
      group_id: null,
      wallet_balance_usd: 88,
      expires_at: '2099-12-30T23:59:58Z'
    })).toBe(false)
    expect(isActiveCreditsWallet({
      status: 'active',
      group_id: null,
      wallet_balance_usd: 88,
      expires_at: '2099-12-30T23:59:59Z'
    })).toBe(true)
  })
})
