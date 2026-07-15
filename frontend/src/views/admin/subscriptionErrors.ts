import { extractApiErrorMessage, extractApiErrorMetadata } from '@/utils/apiError'

type TranslateFn = (key: string) => string

const ASSIGN_CONFLICT_I18N_KEYS: Record<string, string> = {
  wallet_already_active: 'admin.subscriptions.errorWalletAlreadyActive',
  wallet_topup_unsupported: 'admin.subscriptions.errorWalletTopupUnsupported',
  validity_days_mismatch: 'admin.subscriptions.errorAssignConflict',
  notes_mismatch: 'admin.subscriptions.errorAssignConflict',
}

export function getAssignSubscriptionErrorMessage(error: unknown, t: TranslateFn): string {
  const metadata = extractApiErrorMetadata(error)
  const conflictReason = typeof metadata?.conflict_reason === 'string'
    ? metadata.conflict_reason
    : ''
  const conflictKey = ASSIGN_CONFLICT_I18N_KEYS[conflictReason]
  if (conflictKey) return t(conflictKey)
  return extractApiErrorMessage(error, t('admin.subscriptions.failedToAssign'))
}
