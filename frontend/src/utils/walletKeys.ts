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
