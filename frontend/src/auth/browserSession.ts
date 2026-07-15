const BROWSER_SESSION_HEADER = 'X-Sub2API-Browser-Session'
const BROWSER_SESSION_HEADER_VALUE = '1'
const CSRF_HEADER = 'X-CSRF-Token'
const CSRF_COOKIE = 'sub2api_csrf'

let accessToken: string | null = null
const tokenListeners = new Set<(token: string | null) => void>()

const LEGACY_AUTH_STORAGE_KEYS = [
  'auth_token',
  'refresh_token',
  'token_expires_at',
  'auth_user'
] as const

export function clearLegacyPersistedAuth(): void {
  if (typeof localStorage === 'undefined') {
    return
  }
  for (const key of LEGACY_AUTH_STORAGE_KEYS) {
    localStorage.removeItem(key)
  }
}

export function getSessionAccessToken(): string | null {
  return accessToken
}

export function setSessionAccessToken(token: string | null | undefined): void {
  const normalized = typeof token === 'string' ? token.trim() : ''
  accessToken = normalized || null
  for (const listener of tokenListeners) {
    listener(accessToken)
  }
}

export function clearSessionAccessToken(): void {
  setSessionAccessToken(null)
}

export function subscribeSessionAccessToken(listener: (token: string | null) => void): () => void {
  tokenListeners.add(listener)
  return () => tokenListeners.delete(listener)
}

export function getBrowserSessionHeaders(): Record<string, string> {
  const headers: Record<string, string> = {
    [BROWSER_SESSION_HEADER]: BROWSER_SESSION_HEADER_VALUE
  }
  const csrfToken = readCookie(CSRF_COOKIE)
  if (csrfToken) {
    headers[CSRF_HEADER] = csrfToken
  }
  return headers
}

function readCookie(name: string): string | null {
  if (typeof document === 'undefined') {
    return null
  }
  const prefix = `${encodeURIComponent(name)}=`
  for (const segment of document.cookie.split(';')) {
    const value = segment.trim()
    if (value.startsWith(prefix)) {
      return decodeURIComponent(value.slice(prefix.length))
    }
  }
  return null
}

// One-time migration cleanup for browsers that used pre-cookie releases.
clearLegacyPersistedAuth()
