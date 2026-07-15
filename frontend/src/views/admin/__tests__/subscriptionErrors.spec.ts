import { describe, expect, it } from 'vitest'
import { getAssignSubscriptionErrorMessage } from '../subscriptionErrors'

const t = (key: string) => key

describe('admin subscription errors', () => {
  it('reads conflict metadata from the flattened API-client error', () => {
    expect(getAssignSubscriptionErrorMessage({
      reason: 'SUBSCRIPTION_ASSIGNMENT_CONFLICT',
      metadata: { conflict_reason: 'wallet_already_active' },
      message: 'generic backend message',
    }, t)).toBe('admin.subscriptions.errorWalletAlreadyActive')
  })

  it('shows a flattened backend message when no conflict mapping exists', () => {
    expect(getAssignSubscriptionErrorMessage({
      reason: 'MONTHLY_PLAN_WALLET_DISABLED',
      message: '月卡不能写入钱包余额',
    }, t)).toBe('月卡不能写入钱包余额')
  })
})
