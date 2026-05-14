/** 钱包模式多 key 的命名前缀，与后端 SubscriptionActivationService 保持一致。 */
export const WALLET_KEY_NAME_PREFIX = '钱包-'

/**
 * 单 key 模式（5/14 反转决策回归）自动建的钱包通用 key 命名，
 * 必须与后端 service.WalletUniversalAPIKeyName 字符串完全一致。
 */
export const WALLET_UNIVERSAL_KEY_NAME = '钱包通用 key（自动路由）'

/** 单 key 模式 universal key 名匹配，对应后端 IsWalletUniversalKeyName。 */
export function isWalletUniversalKeyName(name: string | null | undefined): boolean {
  return name === WALLET_UNIVERSAL_KEY_NAME
}

/**
 * 判断一个 api_key.name 是否为钱包模式自动建的 key。
 * 命中两类：多 key 模式「钱包-{group}」前缀 + 单 key 模式「钱包通用 key（自动路由）」全名。
 * 用于在 KeysView 给两类 key 都打绿色「钱包」徽章。
 */
export function isWalletKeyName(name: string | null | undefined): boolean {
  if (typeof name !== 'string') {
    return false
  }
  return name.startsWith(WALLET_KEY_NAME_PREFIX) || isWalletUniversalKeyName(name)
}

interface GroupSubmitState {
  isEdit: boolean
  hasActiveWallet: boolean
  walletAnyKey: boolean
  groupId: number | null
}

interface CreateKeyGroupState {
  hasActiveWallet: boolean
  walletAnyKey: boolean
  groupId: number | null
}

interface KeyLike {
  group_id: number | null
}

export function shouldRequireGroupForKeySubmit(state: GroupSubmitState): boolean {
  if (!state.isEdit && state.hasActiveWallet && state.walletAnyKey) {
    return false
  }
  return state.groupId === null
}

export function getCreateKeyGroupId(state: CreateKeyGroupState): number | null {
  if (state.hasActiveWallet && state.walletAnyKey) {
    return null
  }
  return state.groupId
}

export function isWalletUniversalKey(key: KeyLike, hasActiveWallet: boolean): boolean {
  return hasActiveWallet && key.group_id === null
}
