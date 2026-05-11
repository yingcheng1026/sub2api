import { describe, expect, it } from 'vitest'
import {
  getCreateKeyGroupId,
  isWalletUniversalKey,
  shouldRequireGroupForKeySubmit
} from '../walletKeys'

describe('wallet key helpers', () => {
  it('allows a wallet user to create a universal key without selecting a group', () => {
    expect(shouldRequireGroupForKeySubmit({
      isEdit: false,
      hasActiveWallet: true,
      walletAnyKey: true,
      groupId: null
    })).toBe(false)
    expect(getCreateKeyGroupId({
      hasActiveWallet: true,
      walletAnyKey: true,
      groupId: 3
    })).toBeNull()
  })

  it('still requires a group for legacy key creation and edits', () => {
    expect(shouldRequireGroupForKeySubmit({
      isEdit: false,
      hasActiveWallet: false,
      walletAnyKey: false,
      groupId: null
    })).toBe(true)
    expect(shouldRequireGroupForKeySubmit({
      isEdit: true,
      hasActiveWallet: true,
      walletAnyKey: true,
      groupId: null
    })).toBe(true)
  })

  it('marks null-group keys as wallet universal only for active wallet users', () => {
    expect(isWalletUniversalKey({ group_id: null }, true)).toBe(true)
    expect(isWalletUniversalKey({ group_id: null }, false)).toBe(false)
    expect(isWalletUniversalKey({ group_id: 3 }, true)).toBe(false)
  })
})
