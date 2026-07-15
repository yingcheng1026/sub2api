/**
 * Admin Subscriptions API endpoints
 * Handles user subscription management for administrators
 */

import { apiClient } from '../client'
import { postFinancialWrite, type FinancialWriteOptions } from './financialIdempotency'
import type {
  UserSubscription,
  SubscriptionProgress,
  AssignSubscriptionRequest,
  BulkAssignSubscriptionRequest,
  BulkAssignSubscriptionResult,
  ExtendSubscriptionRequest,
  PaginatedResponse
} from '@/types'

export interface AssignSubscriptionOptions {
  idempotencyKey: string
  networkRetries?: 0 | 1
}

export interface PendingSubscriptionAssignment {
  version: 2
  actorId: number
  fingerprint: string
  idempotencyKey: string
  request: AssignSubscriptionRequest
  createdAt: number
}

export type BulkAssignSubscriptionOptions = AssignSubscriptionOptions

export interface PendingBulkSubscriptionAssignment {
  version: 1
  actorId: number
  fingerprint: string
  idempotencyKey: string
  request: BulkAssignSubscriptionRequest
  createdAt: number
}

type SubscriptionAssignmentStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const ASSIGN_IDEMPOTENCY_KEY_PREFIX = 'admin-subscription-assign-'
const PENDING_ASSIGNMENT_STORAGE_KEY_PREFIX = 'admin.subscription.assign.pending.v3'
const MAX_PENDING_ASSIGNMENTS = 20
const PENDING_ASSIGNMENT_RECONCILE_AFTER_MS = 30 * 60 * 1000
const BULK_ASSIGN_IDEMPOTENCY_KEY_PREFIX = 'admin-subscription-bulk-assign-'
const PENDING_BULK_ASSIGNMENT_STORAGE_KEY_PREFIX = 'admin.subscription.bulk-assign.pending.v1'
const PENDING_IDEMPOTENCY_REASONS = new Set([
  'IDEMPOTENCY_IN_PROGRESS',
  'IDEMPOTENCY_RETRY_BACKOFF'
])
let idempotencyFallbackSequence = 0
const inMemoryPendingAssignments = new Map<number, PendingSubscriptionAssignment[]>()
const pendingStorageUnavailableActors = new Set<number>()
const inMemoryPendingBulkAssignments = new Map<number, PendingBulkSubscriptionAssignment[]>()

/**
 * Create a new key for one logical admin subscription assignment.
 * The caller must keep this key for retries of that same request only.
 */
export function createSubscriptionAssignmentIdempotencyKey(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return `${ASSIGN_IDEMPOTENCY_KEY_PREFIX}${globalThis.crypto.randomUUID()}`
  }

  if (typeof globalThis.crypto?.getRandomValues === 'function') {
    const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16))
    const suffix = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
    return `${ASSIGN_IDEMPOTENCY_KEY_PREFIX}${suffix}`
  }

  idempotencyFallbackSequence += 1
  return `${ASSIGN_IDEMPOTENCY_KEY_PREFIX}${Date.now().toString(36)}-${idempotencyFallbackSequence.toString(36)}-${Math.random().toString(36).slice(2)}`
}

export function createBulkSubscriptionAssignmentIdempotencyKey(): string {
  const assignmentKey = createSubscriptionAssignmentIdempotencyKey()
  return assignmentKey.replace(ASSIGN_IDEMPOTENCY_KEY_PREFIX, BULK_ASSIGN_IDEMPOTENCY_KEY_PREFIX)
}

function normalizeAssignmentIdempotencyKey(rawKey: string): string {
  const key = rawKey.trim()
  if (!key) {
    throw new Error('Idempotency-Key is required for subscription assignment')
  }
  if (key.length > 128 || !/^[!-~]+$/.test(key)) {
    throw new Error('Idempotency-Key must contain 1-128 printable ASCII characters')
  }
  return key
}

function assignmentPayloadFingerprint(request: AssignSubscriptionRequest): string {
  return JSON.stringify([
    request.user_id,
    request.plan_id ?? null,
    request.group_id ?? null,
    request.validity_days ?? null,
    request.notes ?? null,
    request.wallet_initial_usd ?? null
  ])
}

function isAssignmentRequest(value: unknown): value is AssignSubscriptionRequest {
  if (typeof value !== 'object' || value === null) return false
  const request = value as Record<string, unknown>
  if (!Number.isInteger(request.user_id) || Number(request.user_id) <= 0) return false

  for (const key of ['plan_id', 'group_id', 'validity_days', 'wallet_initial_usd']) {
    const field = request[key]
    if (field !== undefined && (typeof field !== 'number' || !Number.isFinite(field))) {
      return false
    }
  }
  return request.notes === undefined || typeof request.notes === 'string'
}

function normalizeSubscriptionAssignmentActorID(actorId: number): number {
  if (!Number.isInteger(actorId) || actorId <= 0) {
    throw new Error('Admin actor ID is required for subscription assignment recovery')
  }
  return actorId
}

function pendingSubscriptionAssignmentStorageKey(actorId: number): string {
  return `${PENDING_ASSIGNMENT_STORAGE_KEY_PREFIX}:${normalizeSubscriptionAssignmentActorID(actorId)}`
}

function parsePendingSubscriptionAssignmentValue(
  value: unknown,
  actorId: number
): PendingSubscriptionAssignment | null {
  try {
    const parsed = value as Partial<PendingSubscriptionAssignment>
    if (
      parsed.version !== 2
      || parsed.actorId !== actorId
      || typeof parsed.fingerprint !== 'string'
      || typeof parsed.idempotencyKey !== 'string'
      || typeof parsed.createdAt !== 'number'
      || !Number.isFinite(parsed.createdAt)
      || !isAssignmentRequest(parsed.request)
      || assignmentPayloadFingerprint(parsed.request) !== parsed.fingerprint
    ) {
      return null
    }
    const idempotencyKey = normalizeAssignmentIdempotencyKey(parsed.idempotencyKey)
    return {
      version: 2,
      actorId,
      fingerprint: parsed.fingerprint,
      idempotencyKey,
      request: { ...parsed.request },
      createdAt: parsed.createdAt
    }
  } catch {
    return null
  }
}

function parsePendingSubscriptionAssignments(
  raw: string | null,
  actorId: number
): PendingSubscriptionAssignment[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as unknown
    if (typeof parsed === 'object' && parsed !== null && 'attempts' in parsed) {
      const attempts = (parsed as { attempts?: unknown }).attempts
      const values = Array.isArray(attempts)
        ? attempts
        : (typeof attempts === 'object' && attempts !== null
            ? Object.values(attempts as Record<string, unknown>)
            : [])
      return values
        .map((attempt) => parsePendingSubscriptionAssignmentValue(attempt, actorId))
        .filter((attempt): attempt is PendingSubscriptionAssignment => attempt !== null)
        .sort((a, b) => a.createdAt - b.createdAt)
    }
    return []
  } catch {
    return []
  }
}

function writePendingSubscriptionAssignments(
  attempts: PendingSubscriptionAssignment[],
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): void {
  const normalizedActorID = normalizeSubscriptionAssignmentActorID(actorId)
  const cloned = attempts.map((attempt) => ({
    ...attempt,
    request: { ...attempt.request }
  }))
  inMemoryPendingAssignments.set(normalizedActorID, cloned)
  if (!storage) return
  const storageKey = pendingSubscriptionAssignmentStorageKey(normalizedActorID)
  try {
    if (cloned.length === 0) {
      storage.removeItem(storageKey)
      pendingStorageUnavailableActors.delete(normalizedActorID)
      return
    }
    const attemptsByFingerprint = Object.fromEntries(
      cloned.map((attempt) => [attempt.fingerprint, attempt])
    )
    storage.setItem(storageKey, JSON.stringify({
      version: 3,
      actorId: normalizedActorID,
      attempts: attemptsByFingerprint
    }))
    pendingStorageUnavailableActors.delete(normalizedActorID)
  } catch {
    pendingStorageUnavailableActors.add(normalizedActorID)
    // The in-memory ledger still protects repeated clicks in this page session.
  }
}

export function readPendingSubscriptionAssignments(
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): PendingSubscriptionAssignment[] {
  const normalizedActorID = normalizeSubscriptionAssignmentActorID(actorId)
  if (storage) {
    try {
      const actorPending = inMemoryPendingAssignments.get(normalizedActorID) ?? []
      if (pendingStorageUnavailableActors.has(normalizedActorID) && actorPending.length > 0) {
        const inMemory = actorPending.map((attempt) => ({
          ...attempt,
          request: { ...attempt.request }
        }))
        writePendingSubscriptionAssignments(inMemory, normalizedActorID, storage)
        return inMemory
      }
      const currentRaw = storage.getItem(pendingSubscriptionAssignmentStorageKey(normalizedActorID))
      const stored = parsePendingSubscriptionAssignments(currentRaw, normalizedActorID)
      inMemoryPendingAssignments.set(normalizedActorID, stored)
      return stored
    } catch {
      // Fall back to the in-memory ledger when sessionStorage is unavailable.
    }
  }
  return (inMemoryPendingAssignments.get(normalizedActorID) ?? []).map((attempt) => ({
    ...attempt,
    request: { ...attempt.request }
  }))
}

export function readPendingSubscriptionAssignment(
  actorId: number,
  storage?: SubscriptionAssignmentStorage,
  fingerprint?: string
): PendingSubscriptionAssignment | null {
  const attempts = readPendingSubscriptionAssignments(actorId, storage)
  if (fingerprint) {
    return attempts.find((attempt) => attempt.fingerprint === fingerprint) ?? null
  }
  return attempts.slice().sort((a, b) => a.createdAt - b.createdAt)[0] ?? null
}

export function isPendingSubscriptionAssignmentStale(
  attempt: PendingSubscriptionAssignment,
  now = Date.now()
): boolean {
  return now - attempt.createdAt >= PENDING_ASSIGNMENT_RECONCILE_AFTER_MS
}

export function getOrCreatePendingSubscriptionAssignment(
  request: AssignSubscriptionRequest,
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): PendingSubscriptionAssignment {
  const normalizedActorID = normalizeSubscriptionAssignmentActorID(actorId)
  if (!isAssignmentRequest(request)) {
    throw new Error('Invalid subscription assignment request')
  }
  const fingerprint = assignmentPayloadFingerprint(request)
  const attempts = readPendingSubscriptionAssignments(normalizedActorID, storage)
  const existing = attempts.find((attempt) => attempt.fingerprint === fingerprint)
  if (existing) {
    return existing
  }
  if (attempts.length >= MAX_PENDING_ASSIGNMENTS) {
    throw new Error('Too many unresolved subscription assignments; reconcile them before creating another')
  }

  const pending: PendingSubscriptionAssignment = {
    version: 2,
    actorId: normalizedActorID,
    fingerprint,
    idempotencyKey: createSubscriptionAssignmentIdempotencyKey(),
    request: { ...request },
    createdAt: Date.now()
  }
  writePendingSubscriptionAssignments([...attempts, pending], normalizedActorID, storage)
  return pending
}

export function clearPendingSubscriptionAssignment(
  completedAttempt: Pick<PendingSubscriptionAssignment, 'fingerprint' | 'idempotencyKey'>,
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): void {
  const normalizedActorID = normalizeSubscriptionAssignmentActorID(actorId)
  const attempts = readPendingSubscriptionAssignments(normalizedActorID, storage)
  const remaining = attempts.filter((attempt) => (
    attempt.fingerprint !== completedAttempt.fingerprint
    || attempt.idempotencyKey !== completedAttempt.idempotencyKey
  ))
  if (remaining.length === attempts.length) return
  writePendingSubscriptionAssignments(remaining, normalizedActorID, storage)
}

function normalizeBulkAssignmentActorID(actorId: number): number {
  if (!Number.isInteger(actorId) || actorId <= 0) {
    throw new Error('Admin actor ID is required for bulk subscription assignment recovery')
  }
  return actorId
}

function pendingBulkAssignmentStorageKey(actorId: number): string {
  return `${PENDING_BULK_ASSIGNMENT_STORAGE_KEY_PREFIX}:${normalizeBulkAssignmentActorID(actorId)}`
}

function cloneBulkAssignmentRequest(
  request: BulkAssignSubscriptionRequest
): BulkAssignSubscriptionRequest {
  return { ...request, user_ids: [...request.user_ids] }
}

function bulkAssignmentPayloadFingerprint(request: BulkAssignSubscriptionRequest): string {
  return JSON.stringify([
    request.user_ids,
    request.group_id,
    request.validity_days ?? null,
    request.notes ?? null
  ])
}

function isBulkAssignmentRequest(value: unknown): value is BulkAssignSubscriptionRequest {
  if (typeof value !== 'object' || value === null) return false
  const request = value as Record<string, unknown>
  if (
    !Array.isArray(request.user_ids)
    || request.user_ids.length === 0
    || request.user_ids.some((id) => !Number.isInteger(id) || Number(id) <= 0)
  ) {
    return false
  }
  if (!Number.isInteger(request.group_id) || Number(request.group_id) <= 0) return false
  if (
    request.validity_days !== undefined
    && (!Number.isInteger(request.validity_days) || Number(request.validity_days) <= 0)
  ) {
    return false
  }
  return request.notes === undefined || typeof request.notes === 'string'
}

function parsePendingBulkSubscriptionAssignment(
  value: unknown,
  actorId: number
): PendingBulkSubscriptionAssignment | null {
  try {
    const parsed = value as Partial<PendingBulkSubscriptionAssignment>
    if (
      parsed.version !== 1
      || parsed.actorId !== actorId
      || typeof parsed.fingerprint !== 'string'
      || typeof parsed.idempotencyKey !== 'string'
      || typeof parsed.createdAt !== 'number'
      || !Number.isFinite(parsed.createdAt)
      || !isBulkAssignmentRequest(parsed.request)
      || bulkAssignmentPayloadFingerprint(parsed.request) !== parsed.fingerprint
    ) {
      return null
    }
    return {
      version: 1,
      actorId,
      fingerprint: parsed.fingerprint,
      idempotencyKey: normalizeAssignmentIdempotencyKey(parsed.idempotencyKey),
      request: cloneBulkAssignmentRequest(parsed.request),
      createdAt: parsed.createdAt
    }
  } catch {
    return null
  }
}

function parsePendingBulkSubscriptionAssignments(
  raw: string | null,
  actorId: number
): PendingBulkSubscriptionAssignment[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as { attempts?: unknown }
    if (typeof parsed !== 'object' || parsed === null || typeof parsed.attempts !== 'object' || parsed.attempts === null) {
      return []
    }
    return Object.values(parsed.attempts as Record<string, unknown>)
      .map((attempt) => parsePendingBulkSubscriptionAssignment(attempt, actorId))
      .filter((attempt): attempt is PendingBulkSubscriptionAssignment => attempt !== null)
      .sort((a, b) => a.createdAt - b.createdAt)
  } catch {
    return []
  }
}

function writePendingBulkSubscriptionAssignments(
  attempts: PendingBulkSubscriptionAssignment[],
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): void {
  const cloned = attempts.map((attempt) => ({
    ...attempt,
    request: cloneBulkAssignmentRequest(attempt.request)
  }))
  inMemoryPendingBulkAssignments.set(actorId, cloned)
  if (!storage) return

  const storageKey = pendingBulkAssignmentStorageKey(actorId)
  try {
    if (cloned.length === 0) {
      storage.removeItem(storageKey)
      return
    }
    const attemptsByFingerprint = Object.fromEntries(
      cloned.map((attempt) => [attempt.fingerprint, attempt])
    )
    storage.setItem(storageKey, JSON.stringify({ version: 1, attempts: attemptsByFingerprint }))
  } catch {
    // Keep the actor-scoped in-memory map so retries in this page session stay safe.
  }
}

export function readPendingBulkSubscriptionAssignments(
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): PendingBulkSubscriptionAssignment[] {
  const normalizedActorID = normalizeBulkAssignmentActorID(actorId)
  if (storage) {
    try {
      const raw = storage.getItem(pendingBulkAssignmentStorageKey(normalizedActorID))
      if (raw) {
        const stored = parsePendingBulkSubscriptionAssignments(raw, normalizedActorID)
        inMemoryPendingBulkAssignments.set(normalizedActorID, stored)
        return stored
      }
    } catch {
      // Fall back to the actor-scoped in-memory map.
    }
  }
  return (inMemoryPendingBulkAssignments.get(normalizedActorID) ?? []).map((attempt) => ({
    ...attempt,
    request: cloneBulkAssignmentRequest(attempt.request)
  }))
}

export function getOrCreatePendingBulkSubscriptionAssignment(
  request: BulkAssignSubscriptionRequest,
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): PendingBulkSubscriptionAssignment {
  const normalizedActorID = normalizeBulkAssignmentActorID(actorId)
  if (!isBulkAssignmentRequest(request)) {
    throw new Error('Invalid bulk subscription assignment request')
  }
  const fingerprint = bulkAssignmentPayloadFingerprint(request)
  const attempts = readPendingBulkSubscriptionAssignments(normalizedActorID, storage)
  const existing = attempts.find((attempt) => attempt.fingerprint === fingerprint)
  if (existing) return existing
  if (attempts.length >= MAX_PENDING_ASSIGNMENTS) {
    throw new Error('Too many unresolved bulk subscription assignments; reconcile them before creating another')
  }

  const pending: PendingBulkSubscriptionAssignment = {
    version: 1,
    actorId: normalizedActorID,
    fingerprint,
    idempotencyKey: createBulkSubscriptionAssignmentIdempotencyKey(),
    request: cloneBulkAssignmentRequest(request),
    createdAt: Date.now()
  }
  writePendingBulkSubscriptionAssignments([...attempts, pending], normalizedActorID, storage)
  return pending
}

export function clearPendingBulkSubscriptionAssignment(
  completedAttempt: Pick<PendingBulkSubscriptionAssignment, 'fingerprint' | 'idempotencyKey'>,
  actorId: number,
  storage?: SubscriptionAssignmentStorage
): void {
  const normalizedActorID = normalizeBulkAssignmentActorID(actorId)
  const attempts = readPendingBulkSubscriptionAssignments(normalizedActorID, storage)
  const remaining = attempts.filter((attempt) => (
    attempt.fingerprint !== completedAttempt.fingerprint
    || attempt.idempotencyKey !== completedAttempt.idempotencyKey
  ))
  if (remaining.length === attempts.length) return
  writePendingBulkSubscriptionAssignments(remaining, normalizedActorID, storage)
}

function errorRecord(error: unknown): Record<string, unknown> | null {
  return typeof error === 'object' && error !== null
    ? error as Record<string, unknown>
    : null
}

export function isAmbiguousSubscriptionAssignmentError(error: unknown): boolean {
  const record = errorRecord(error)
  if (!record) return false
  if (record.status === 0) return true

  const code = typeof record.code === 'string' ? record.code.toUpperCase() : ''
  if (code === 'ERR_NETWORK' || code === 'ECONNABORTED' || code === 'ETIMEDOUT') {
    return true
  }
  return record.isAxiosError === true && !record.response
}

export function shouldRetainPendingSubscriptionAssignment(error: unknown): boolean {
  if (isAmbiguousSubscriptionAssignmentError(error)) return true
  const record = errorRecord(error)
  if (!record) return false

  const response = errorRecord(record.response)
  const responseData = errorRecord(response?.data)
  const statusValue = record.status ?? response?.status
  const status = typeof statusValue === 'number' ? statusValue : Number(statusValue)
  if (Number.isFinite(status) && status >= 500) return true

  const reasonValue = record.reason ?? record.code ?? responseData?.reason ?? responseData?.code
  const reason = typeof reasonValue === 'string' ? reasonValue.toUpperCase() : ''
  return PENDING_IDEMPOTENCY_REASONS.has(reason)
}

/**
 * List all subscriptions with pagination
 * @param page - Page number (default: 1)
 * @param pageSize - Items per page (default: 20)
 * @param filters - Optional filters (status, user_id, group_id, sort_by, sort_order)
 * @returns Paginated list of subscriptions
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    status?: 'active' | 'expired' | 'revoked'
    user_id?: number
    group_id?: number
    platform?: string
    sort_by?: string
    sort_order?: 'asc' | 'desc'
  },
  options?: {
    signal?: AbortSignal
  }
): Promise<PaginatedResponse<UserSubscription>> {
  const { data } = await apiClient.get<PaginatedResponse<UserSubscription>>(
    '/admin/subscriptions',
    {
      params: {
        page,
        page_size: pageSize,
        ...filters
      },
      signal: options?.signal
    }
  )
  return data
}

/**
 * Get subscription by ID
 * @param id - Subscription ID
 * @returns Subscription details
 */
export async function getById(id: number): Promise<UserSubscription> {
  const { data } = await apiClient.get<UserSubscription>(`/admin/subscriptions/${id}`)
  return data
}

/**
 * Get subscription progress
 * @param id - Subscription ID
 * @returns Subscription progress with usage stats
 */
export async function getProgress(id: number): Promise<SubscriptionProgress> {
  const { data } = await apiClient.get<SubscriptionProgress>(`/admin/subscriptions/${id}/progress`)
  return data
}

/**
 * Assign subscription to user
 * @param request - Assignment request
 * @param options - Idempotency key and bounded ambiguous-network retry policy
 * @returns Created subscription
 */
export async function assign(
  request: AssignSubscriptionRequest,
  options: AssignSubscriptionOptions
): Promise<UserSubscription> {
  const idempotencyKey = normalizeAssignmentIdempotencyKey(options.idempotencyKey)
  const config = { headers: { 'Idempotency-Key': idempotencyKey } }

  try {
    const { data } = await apiClient.post<UserSubscription>(
      '/admin/subscriptions/assign',
      request,
      config
    )
    return data
  } catch (error: unknown) {
    if (options.networkRetries === 0 || !isAmbiguousSubscriptionAssignmentError(error)) {
      throw error
    }

    const { data } = await apiClient.post<UserSubscription>(
      '/admin/subscriptions/assign',
      request,
      config
    )
    return data
  }
}

/**
 * Bulk assign subscriptions to multiple users
 * @param request - Bulk assignment request
 * @returns Created subscriptions
 */
export async function bulkAssign(
  request: BulkAssignSubscriptionRequest,
  options: BulkAssignSubscriptionOptions
): Promise<BulkAssignSubscriptionResult> {
  const idempotencyKey = normalizeAssignmentIdempotencyKey(options.idempotencyKey)
  const config = { headers: { 'Idempotency-Key': idempotencyKey } }

  try {
    const { data } = await apiClient.post<BulkAssignSubscriptionResult>(
      '/admin/subscriptions/bulk-assign',
      request,
      config
    )
    return data
  } catch (error: unknown) {
    if (options.networkRetries === 0 || !isAmbiguousSubscriptionAssignmentError(error)) {
      throw error
    }
    const { data } = await apiClient.post<BulkAssignSubscriptionResult>(
      '/admin/subscriptions/bulk-assign',
      request,
      config
    )
    return data
  }
}

/**
 * Extend subscription validity
 * @param id - Subscription ID
 * @param request - Extension request with days
 * @returns Updated subscription
 */
export async function extend(
  id: number,
  request: ExtendSubscriptionRequest,
  options: FinancialWriteOptions = {}
): Promise<UserSubscription> {
  return postFinancialWrite<UserSubscription>(
    `/admin/subscriptions/${id}/extend`,
    request,
    'admin-subscription-extend',
    options
  )
}

/**
 * Revoke subscription
 * @param id - Subscription ID
 * @returns Success confirmation
 */
export async function revoke(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/admin/subscriptions/${id}`)
  return data
}

/**
 * Reset daily, weekly, and/or monthly usage quota for a subscription
 * @param id - Subscription ID
 * @param options - Which windows to reset
 * @returns Updated subscription
 */
export async function resetQuota(
  id: number,
  options: { daily: boolean; weekly: boolean; monthly: boolean }
): Promise<UserSubscription> {
  const { data } = await apiClient.post<UserSubscription>(
    `/admin/subscriptions/${id}/reset-quota`,
    options
  )
  return data
}

/**
 * List subscriptions by group
 * @param groupId - Group ID
 * @param page - Page number
 * @param pageSize - Items per page
 * @returns Paginated list of subscriptions in the group
 */
export async function listByGroup(
  groupId: number,
  page: number = 1,
  pageSize: number = 20
): Promise<PaginatedResponse<UserSubscription>> {
  const { data } = await apiClient.get<PaginatedResponse<UserSubscription>>(
    `/admin/groups/${groupId}/subscriptions`,
    {
      params: { page, page_size: pageSize }
    }
  )
  return data
}

/**
 * List subscriptions by user
 * @param userId - User ID
 * @param page - Page number
 * @param pageSize - Items per page
 * @returns Paginated list of user's subscriptions
 */
export async function listByUser(
  userId: number,
  page: number = 1,
  pageSize: number = 20
): Promise<PaginatedResponse<UserSubscription>> {
  const { data } = await apiClient.get<PaginatedResponse<UserSubscription>>(
    `/admin/users/${userId}/subscriptions`,
    {
      params: { page, page_size: pageSize }
    }
  )
  return data
}

export const subscriptionsAPI = {
  list,
  getById,
  getProgress,
  assign,
  bulkAssign,
  extend,
  revoke,
  resetQuota,
  listByGroup,
  listByUser
}

export default subscriptionsAPI
