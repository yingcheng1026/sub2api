import { apiClient } from '../client'

export interface FinancialWriteOptions {
  idempotencyKey?: string
  networkRetries?: 0 | 1
}

let fallbackSequence = 0

export function createFinancialIdempotencyKey(prefix: string): string {
  const normalizedPrefix = prefix.trim().replace(/[^A-Za-z0-9._-]+/g, '-')
  if (!normalizedPrefix || normalizedPrefix.length > 72) {
    throw new Error('Invalid financial idempotency key prefix')
  }

  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return `${normalizedPrefix}-${globalThis.crypto.randomUUID()}`
  }

  if (typeof globalThis.crypto?.getRandomValues === 'function') {
    const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16))
    const suffix = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
    return `${normalizedPrefix}-${suffix}`
  }

  fallbackSequence += 1
  return `${normalizedPrefix}-${Date.now().toString(36)}-${fallbackSequence.toString(36)}-${Math.random().toString(36).slice(2)}`
}

export function normalizeFinancialIdempotencyKey(rawKey: string): string {
  const key = rawKey.trim()
  if (!key) {
    throw new Error('Idempotency-Key is required for financial writes')
  }
  if (key.length > 128 || !/^[!-~]+$/.test(key)) {
    throw new Error('Idempotency-Key must contain 1-128 printable ASCII characters')
  }
  return key
}

export function isAmbiguousFinancialWriteError(error: unknown): boolean {
  if (typeof error !== 'object' || error === null) return false
  const record = error as Record<string, unknown>
  if (record.status === 0) return true
  const code = typeof record.code === 'string' ? record.code.toUpperCase() : ''
  if (code === 'ERR_NETWORK' || code === 'ECONNABORTED' || code === 'ETIMEDOUT') {
    return true
  }
  return record.isAxiosError === true && !record.response
}

export async function postFinancialWrite<T>(
  path: string,
  payload: unknown,
  keyPrefix: string,
  options: FinancialWriteOptions = {}
): Promise<T> {
  const idempotencyKey = normalizeFinancialIdempotencyKey(
    options.idempotencyKey ?? createFinancialIdempotencyKey(keyPrefix)
  )
  const config = { headers: { 'Idempotency-Key': idempotencyKey } }

  try {
    const { data } = await apiClient.post<T>(path, payload, config)
    return data
  } catch (error: unknown) {
    if (options.networkRetries === 0 || !isAmbiguousFinancialWriteError(error)) {
      throw error
    }
    const { data } = await apiClient.post<T>(path, payload, config)
    return data
  }
}
