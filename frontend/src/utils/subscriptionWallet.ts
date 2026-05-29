import type { UserSubscription } from '@/types'

type WalletLikeSubscription = Pick<UserSubscription, 'wallet_balance_usd' | 'wallet_initial_usd'>

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
  return Math.max(0, (used / initial) * 100)
}

