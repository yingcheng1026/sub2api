import type { UserSubscription } from '@/types'

type WalletLikeSubscription = Pick<UserSubscription, 'wallet_balance_usd' | 'wallet_initial_usd'>

interface CreditsWalletLikeSubscription extends WalletLikeSubscription {
  status?: string
  expires_at?: string | null
  group_id?: number | null
}

// Keep this exactly aligned with backend MaxExpiresAt.Add(-24h).
const PERMANENT_CREDITS_WALLET_THRESHOLD_MS = Date.parse('2099-12-30T23:59:59Z')

export const ANTI_OVERWRITE_WALLET_MARKER = 'hasWalletBalance'

export function hasWalletBalance(subscription: WalletLikeSubscription | null | undefined): boolean {
  return subscription?.wallet_balance_usd != null
}

export function getWalletRemainingUSD(subscription: WalletLikeSubscription): number {
  return Number(subscription.wallet_balance_usd ?? 0)
}

export function getWalletInitialUSD(subscription: WalletLikeSubscription): number {
  return Number(subscription.wallet_initial_usd ?? 0)
}

export function getWalletUsedPercent(subscription: WalletLikeSubscription): number {
  const initial = getWalletInitialUSD(subscription)
  if (initial <= 0) return 0
  const used = initial - getWalletRemainingUSD(subscription)
  return Math.min(100, Math.max(0, (used / initial) * 100))
}

export function isActiveCreditsWallet(
  subscription: CreditsWalletLikeSubscription | null | undefined,
): boolean {
  if (
    subscription?.status !== 'active'
    || subscription.group_id !== null
    || !hasWalletBalance(subscription)
  ) return false
  const expiresAt = Date.parse(subscription.expires_at || '')
  return Number.isFinite(expiresAt) && expiresAt >= PERMANENT_CREDITS_WALLET_THRESHOLD_MS
}

export function getActiveCreditsWalletBalanceUSD(
  subscriptions: readonly CreditsWalletLikeSubscription[],
): number {
  const wallet = subscriptions.find(isActiveCreditsWallet)
  return wallet ? getWalletRemainingUSD(wallet) : 0
}
