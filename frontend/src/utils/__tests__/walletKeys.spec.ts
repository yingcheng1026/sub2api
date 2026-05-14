import { describe, expect, it } from 'vitest'
import {
  WALLET_KEY_NAME_PREFIX,
  WALLET_UNIVERSAL_KEY_NAME,
  getCreateKeyGroupId,
  isWalletKeyName,
  isWalletUniversalKey,
  isWalletUniversalKeyName,
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

  // B2.6：多 key 命名「钱包-{group}」靠 isWalletKeyName 识别。
  it('detects wallet keys by their 钱包- prefix', () => {
    expect(WALLET_KEY_NAME_PREFIX).toBe('钱包-')
    expect(isWalletKeyName('钱包-gpt-5')).toBe(true)
    expect(isWalletKeyName('钱包-claude-sonnet')).toBe(true)
    expect(isWalletKeyName('my-key')).toBe(false)
    expect(isWalletKeyName('')).toBe(false)
    expect(isWalletKeyName(null)).toBe(false)
    expect(isWalletKeyName(undefined)).toBe(false)
  })

  // 5/14 反转决策：单 key 模式 universal key 也走 isWalletKeyName 显示徽章。
  it('also matches single-key universal name', () => {
    expect(WALLET_UNIVERSAL_KEY_NAME).toBe('钱包通用 key（自动路由）')
    expect(isWalletUniversalKeyName(WALLET_UNIVERSAL_KEY_NAME)).toBe(true)
    expect(isWalletUniversalKeyName('钱包-gpt-5')).toBe(false)
    expect(isWalletUniversalKeyName(null)).toBe(false)
    // isWalletKeyName 同时覆盖多 key 前缀和 universal key 全名
    expect(isWalletKeyName(WALLET_UNIVERSAL_KEY_NAME)).toBe(true)
  })
})
