import type { UserSubscription } from '@/types'

export type LockedRateMap = Record<number, number>

export function lockedRatesFromSubscriptions(subscriptions: UserSubscription[] | null | undefined): LockedRateMap {
  const out: LockedRateMap = {}
  for (const sub of subscriptions || []) {
    if (!sub || sub.status !== 'active' || !sub.locked_rates) continue
    for (const [groupID, rate] of Object.entries(sub.locked_rates)) {
      const id = Number(groupID)
      if (Number.isFinite(id) && typeof rate === 'number' && rate >= 0) {
        out[id] = rate
      }
    }
  }
  return out
}

export function lockedRateForGroup(lockedRates: LockedRateMap, groupID: number): number | null {
  return lockedRates[groupID] ?? null
}
