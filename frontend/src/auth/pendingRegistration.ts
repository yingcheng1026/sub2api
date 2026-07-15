export type PendingAuthTokenField = 'pending_auth_token' | 'pending_oauth_token'

export interface PendingRegistration {
  email: string
  password: string
  turnstile_token?: string
  promo_code?: string
  invitation_code?: string
  device_fingerprint?: string
  aff_code?: string
  pending_auth_token?: string
  pending_auth_token_field?: PendingAuthTokenField
  pending_provider?: string
  pending_redirect?: string
  pending_adoption_decision?: {
    adopt_display_name?: boolean
    adopt_avatar?: boolean
  }
}

let pendingRegistration: PendingRegistration | null = null

/**
 * Holds the password only in this JavaScript realm while the router moves to
 * the verification page. A reload intentionally expires the registration flow.
 */
export function setPendingRegistration(data: PendingRegistration): void {
  pendingRegistration = { ...data }
}

/** One-time read so another route cannot replay registration credentials. */
export function consumePendingRegistration(): PendingRegistration | null {
  const data = pendingRegistration
  pendingRegistration = null
  return data ? { ...data } : null
}

export function clearPendingRegistration(): void {
  pendingRegistration = null
}
