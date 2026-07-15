import { describe, expect, it } from 'vitest'
import {
  WALLET_KEY_NAME_PREFIX,
  WALLET_UNIVERSAL_KEY_NAME,
  filterGroupsForWalletKeySelection,
  getCreateKeyGroupId,
  isWalletKeyName,
  isSystemManagedWalletKey,
  isWalletUniversalKey,
  isWalletUniversalKeyName,
  shouldRequireGroupForKeySubmit
} from '../walletKeys'

describe('wallet key helpers', () => {
  it('requires users to choose a real group instead of creating arbitrary null-group keys', () => {
    expect(shouldRequireGroupForKeySubmit({
      isEdit: false,
      hasActiveWallet: true,
      walletAnyKey: true,
      groupId: null
    })).toBe(true)
    expect(getCreateKeyGroupId({
      hasActiveWallet: true,
      walletAnyKey: true,
      groupId: 3
    })).toBe(3)
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

  it('marks only the backend-created named null-group key as wallet universal', () => {
    expect(isWalletUniversalKey({ purpose: 'wallet_universal', name: WALLET_UNIVERSAL_KEY_NAME, group_id: null }, true)).toBe(true)
    expect(isWalletUniversalKey({ purpose: 'standard', name: WALLET_UNIVERSAL_KEY_NAME, group_id: null }, true)).toBe(false)
    expect(isWalletUniversalKey({ purpose: 'wallet_universal', name: '普通 key', group_id: null }, true)).toBe(false)
    expect(isWalletUniversalKey({ purpose: 'wallet_universal', name: WALLET_UNIVERSAL_KEY_NAME, group_id: null }, false)).toBe(false)
    expect(isWalletUniversalKey({ purpose: 'wallet_universal', name: WALLET_UNIVERSAL_KEY_NAME, group_id: 3 }, true)).toBe(false)
  })

  it('protects only the immutable backend-managed wallet key identity', () => {
    expect(isSystemManagedWalletKey({ purpose: 'wallet_universal', name: WALLET_UNIVERSAL_KEY_NAME, group_id: null })).toBe(true)
    expect(isSystemManagedWalletKey({ purpose: 'standard', name: WALLET_UNIVERSAL_KEY_NAME, group_id: null })).toBe(false)
    expect(isSystemManagedWalletKey({ purpose: 'wallet_universal', name: WALLET_UNIVERSAL_KEY_NAME, group_id: 3 })).toBe(false)
    expect(isSystemManagedWalletKey({ purpose: 'wallet_universal', name: '普通 key', group_id: null })).toBe(false)
  })

  it('limits wallet-user fixed keys to exact openai-default and authorized exact vip groups', () => {
    const groups = [
      { id: 3, name: 'openai-default', platform: 'openai', is_exclusive: false, subscription_type: 'standard' },
      { id: 22, name: 'vip', platform: 'anthropic', is_exclusive: true, subscription_type: 'standard' },
      { id: 23, name: 'vip', platform: 'openai', is_exclusive: true, subscription_type: 'standard' },
      { id: 24, name: 'other-public', platform: 'openai', is_exclusive: false, subscription_type: 'standard' },
      { id: 25, name: 'vip', platform: 'anthropic', is_exclusive: false, subscription_type: 'standard' },
      { id: 26, name: 'vip', platform: 'anthropic', is_exclusive: true, subscription_type: 'subscription' },
    ]

    expect(filterGroupsForWalletKeySelection(groups, true).map((group) => group.id)).toEqual([3, 22])
    expect(filterGroupsForWalletKeySelection(groups.filter((group) => group.id !== 22), true).map((group) => group.id)).toEqual([3])
    expect(filterGroupsForWalletKeySelection(groups, false)).toEqual(groups)
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
