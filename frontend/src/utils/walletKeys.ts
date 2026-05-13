/** 钱包模式多 key 的命名前缀，与后端 SubscriptionActivationService 保持一致。 */
export const WALLET_KEY_NAME_PREFIX = '钱包-'

/** 判断一个 api_key.name 是否为钱包模式自动建的 key（命名「钱包-{group}」）。 */
export function isWalletKeyName(name: string | null | undefined): boolean {
  return typeof name === 'string' && name.startsWith(WALLET_KEY_NAME_PREFIX)
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
