import type {
  CreateOrderRequest,
  CreateOrderResult,
  MethodLimit,
  OrderType,
  WechatJSAPIPayload,
  WechatOAuthInfo,
} from '@/types/payment'

export const PAYMENT_RECOVERY_STORAGE_KEY = 'payment.recovery.current'
export const PAYMENT_RECOVERY_SESSION_STORAGE_KEY = 'payment.recovery.launch.current'
export const PAYMENT_RECOVERY_RETENTION_MS = 24 * 60 * 60 * 1000
export const PAYMENT_LATE_SETTLEMENT_GRACE_MS = 5 * 60 * 1000

const PAYMENT_PROCESSING_STATUSES = new Set([
  'PENDING',
  'CREATED',
  'WAITING',
  'PROCESSING',
  'PAID',
  'RECHARGING',
])
const PAYMENT_FULFILLMENT_PENDING_STATUSES = new Set(['PAID', 'RECHARGING'])
const PAYMENT_TERMINAL_FAILURE_STATUSES = new Set(['CANCELLED', 'EXPIRED'])

function normalizePaymentStatus(status: string | null | undefined): string {
  return String(status || '').trim().toUpperCase()
}

/** The wallet/subscription has been fulfilled only after the backend commits COMPLETED. */
export function isPaymentCompleted(status: string | null | undefined): boolean {
  return normalizePaymentStatus(status) === 'COMPLETED'
}

/** Provider payment can be captured before wallet/subscription fulfillment finishes. */
export function isPaymentStillProcessing(status: string | null | undefined): boolean {
  return PAYMENT_PROCESSING_STATUSES.has(normalizePaymentStatus(status))
}

/** Payment was captured and must keep polling even after the provider deadline. */
export function isPaymentFulfillmentPending(
  status: string | null | undefined,
  paidAt?: string | null
): boolean {
  const normalizedStatus = normalizePaymentStatus(status)
  return PAYMENT_FULFILLMENT_PENDING_STATUSES.has(normalizedStatus)
    || (normalizedStatus === 'FAILED' && hasPaymentCaptureEvidence(paidAt))
}

/** Provider payment succeeded but wallet/subscription fulfillment needs recovery. */
export function isPaymentFulfillmentFailed(
  status: string | null | undefined,
  paidAt?: string | null
): boolean {
  return normalizePaymentStatus(status) === 'FAILED' && hasPaymentCaptureEvidence(paidAt)
}

export function isPaymentTerminalFailure(
  status: string | null | undefined,
  paidAt?: string | null
): boolean {
  const normalizedStatus = normalizePaymentStatus(status)
  return PAYMENT_TERMINAL_FAILURE_STATUSES.has(normalizedStatus)
    || (normalizedStatus === 'FAILED' && !hasPaymentCaptureEvidence(paidAt))
}

/**
 * The backend accepts a late provider success for an expired order for a short
 * grace period. During that window EXPIRED is not yet safe to present as an
 * irreversible failure because the provider webhook can still complete it.
 */
export function isPaymentLateSettlementRecoverable(
  status: string | null | undefined,
  _paidAt: string | null | undefined,
  expiresAt: string | null | undefined,
  now = Date.now(),
): boolean {
  if (normalizePaymentStatus(status) !== 'EXPIRED') return false

  const expiresAtMs = Date.parse(String(expiresAt || ''))
  if (!Number.isFinite(expiresAtMs)) return false
  return now <= expiresAtMs + PAYMENT_LATE_SETTLEMENT_GRACE_MS
}

function hasPaymentCaptureEvidence(paidAt: string | null | undefined): boolean {
  return typeof paidAt === 'string' && paidAt.trim() !== ''
}

const VISIBLE_METHOD_ALIASES = {
  alipay: 'alipay',
  alipay_direct: 'alipay',
  wxpay: 'wxpay',
  wxpay_direct: 'wxpay',
  stripe: 'stripe',
} as const

export type VisiblePaymentMethod = 'alipay' | 'wxpay' | 'stripe'
export type StripeVisibleMethod = 'alipay' | 'wechat_pay'
export type PaymentLaunchKind =
  | 'qr_waiting'
  | 'redirect_waiting'
  | 'stripe_popup'
  | 'stripe_route'
  | 'wechat_oauth'
  | 'wechat_jsapi'
  | 'unhandled'

export interface PaymentRecoverySnapshot {
  orderId: number
  userId?: number
  revision?: string
  amount: number
  qrCode: string
  expiresAt: string
  paymentType: string
  payUrl: string
  outTradeNo: string
  clientSecret: string
  payAmount: number
  orderType: OrderType | ''
  paymentMode: string
  resumeToken: string
  createdAt: number
}

export interface PaymentLaunchContext {
  visibleMethod: string
  orderType: OrderType
  isMobile: boolean
  isWechatBrowser?: boolean
  now?: number
  stripePopupUrl?: string
  stripeRouteUrl?: string
  userId?: number
  revision?: string
}

export interface PaymentLaunchDecision {
  kind: PaymentLaunchKind
  paymentState: PaymentRecoverySnapshot
  recovery: PaymentRecoverySnapshot
  stripeMethod?: StripeVisibleMethod
  oauth?: WechatOAuthInfo
  jsapi?: WechatJSAPIPayload
}

export interface BuildCreateOrderPayloadInput {
  amount: number
  paymentType: string
  orderType: OrderType
  planId?: number
  origin?: string
  isMobile: boolean
  isWechatBrowser: boolean
}

type CreateOrderFlowResult = CreateOrderResult & {
  resume_token?: string
}

type RecoveryStorage = Pick<Storage, 'getItem' | 'removeItem' | 'setItem'>

interface PaymentRecoveryEnvelope {
  version: 2
  entries: PaymentRecoverySnapshot[]
}

export interface PaymentRecoverySelector {
  userId: number
  orderId: number
  revision: string
}

export function normalizeVisibleMethod(method: string): VisiblePaymentMethod | '' {
  const normalized = VISIBLE_METHOD_ALIASES[method.trim() as keyof typeof VISIBLE_METHOD_ALIASES]
  return normalized ?? ''
}

export function getVisibleMethods(methods: Record<string, MethodLimit>): Record<string, MethodLimit> {
  const visible: Record<string, MethodLimit> = {}

  Object.entries(methods).forEach(([type, limit]) => {
    const normalized = normalizeVisibleMethod(type)
    if (!normalized) return

    const isCanonical = type === normalized
    const existing = visible[normalized]
    if (!existing || isCanonical) {
      visible[normalized] = { ...limit }
    }
  })

  return visible
}

export function buildCreateOrderPayload(input: BuildCreateOrderPayloadInput): CreateOrderRequest {
  const visibleMethod = normalizeVisibleMethod(input.paymentType) || input.paymentType.trim()
  const normalizedOrigin = (input.origin || '').trim().replace(/\/+$/, '')
  const payload: CreateOrderRequest = {
    amount: input.amount,
    payment_type: visibleMethod,
    order_type: input.orderType,
    is_mobile: input.isMobile,
    payment_source: visibleMethod === 'wxpay' && input.isWechatBrowser
      ? 'wechat_in_app_resume'
      : 'hosted_redirect',
  }

  if (input.planId) {
    payload.plan_id = input.planId
  }
  if (normalizedOrigin) {
    payload.return_url = `${normalizedOrigin}/payment/result`
  }

  return payload
}

export function decidePaymentLaunch(
  result: CreateOrderFlowResult,
  context: PaymentLaunchContext,
): PaymentLaunchDecision {
  const visibleMethod = normalizeVisibleMethod(context.visibleMethod) || context.visibleMethod
  const baseState = createPaymentRecoverySnapshot({
    orderId: result.order_id,
    userId: context.userId,
    revision: context.revision,
    amount: result.amount,
    qrCode: result.qr_code || '',
    expiresAt: result.expires_at || '',
    paymentType: visibleMethod,
    payUrl: result.pay_url || '',
    outTradeNo: result.out_trade_no || '',
    clientSecret: result.client_secret || '',
    payAmount: result.pay_amount,
    orderType: context.orderType,
    paymentMode: (result.payment_mode || '').trim(),
    resumeToken: result.resume_token || '',
  }, context.now)

  if (baseState.clientSecret) {
    // visibleMethod === 'stripe' means the user clicked the dedicated Stripe button
    // and should land on the full Payment Element to choose a sub-method themselves.
    const isStripeButton = visibleMethod === 'stripe'
    const stripeMethod: StripeVisibleMethod | undefined = isStripeButton
      ? undefined
      : visibleMethod === 'wxpay' ? 'wechat_pay' : 'alipay'
    const kind: PaymentLaunchKind = stripeMethod === 'alipay' && !context.isMobile
      ? 'stripe_popup'
      : 'stripe_route'
    const payUrl = kind === 'stripe_popup'
      ? context.stripePopupUrl || context.stripeRouteUrl || ''
      : context.stripeRouteUrl || context.stripePopupUrl || ''
    const paymentState = { ...baseState, payUrl }
    return { kind, paymentState, recovery: paymentState, stripeMethod }
  }

  if (result.result_type === 'oauth_required' && result.oauth?.authorize_url) {
    return { kind: 'wechat_oauth', paymentState: baseState, recovery: baseState, oauth: result.oauth }
  }

  const jsapiPayload = result.jsapi ?? result.jsapi_payload
  if (result.result_type === 'jsapi_ready' && jsapiPayload) {
    return { kind: 'wechat_jsapi', paymentState: baseState, recovery: baseState, jsapi: jsapiPayload }
  }

  const normalizedPaymentMode = baseState.paymentMode.trim().toLowerCase()
  const prefersRedirect = normalizedPaymentMode === 'redirect'
    || normalizedPaymentMode === 'popup'
    || (context.isMobile && !!baseState.payUrl)
  const prefersQr = normalizedPaymentMode === 'qrcode'
    || normalizedPaymentMode === 'native'
    || (!prefersRedirect && !!baseState.qrCode)

  if (visibleMethod === 'wxpay' && context.isWechatBrowser && baseState.payUrl && !baseState.qrCode) {
    return { kind: 'redirect_waiting', paymentState: baseState, recovery: baseState }
  }

  if (prefersRedirect && baseState.payUrl) {
    return { kind: 'redirect_waiting', paymentState: baseState, recovery: baseState }
  }

  if (prefersQr && baseState.qrCode) {
    return { kind: 'qr_waiting', paymentState: baseState, recovery: baseState }
  }

  if (baseState.payUrl) {
    return { kind: 'redirect_waiting', paymentState: baseState, recovery: baseState }
  }

  return { kind: 'unhandled', paymentState: baseState, recovery: baseState }
}

export function createPaymentRecoverySnapshot(
  state: Omit<PaymentRecoverySnapshot, 'createdAt'>,
  now = Date.now(),
): PaymentRecoverySnapshot {
  return {
    ...state,
    revision: state.revision || createRecoveryRevision(),
    createdAt: now,
  }
}

function createRecoveryRevision(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
}

function recoveryEntryKey(snapshot: Pick<PaymentRecoverySnapshot, 'userId' | 'orderId' | 'revision'>): string {
  return `${snapshot.userId || 0}:${snapshot.orderId}:${snapshot.revision || ''}`
}

function normalizeRecoverySnapshot(parsed: Partial<PaymentRecoverySnapshot>): PaymentRecoverySnapshot | null {
  if (
    typeof parsed.orderId !== 'number'
    || !Number.isSafeInteger(parsed.orderId)
    || parsed.orderId <= 0
    || typeof parsed.amount !== 'number'
    || typeof parsed.qrCode !== 'string'
    || typeof parsed.expiresAt !== 'string'
    || typeof parsed.paymentType !== 'string'
    || typeof parsed.payUrl !== 'string'
    || (parsed.outTradeNo != null && typeof parsed.outTradeNo !== 'string')
    || typeof parsed.clientSecret !== 'string'
    || typeof parsed.payAmount !== 'number'
    || typeof parsed.paymentMode !== 'string'
    || typeof parsed.resumeToken !== 'string'
    || typeof parsed.createdAt !== 'number'
    || (parsed.userId != null && (!Number.isSafeInteger(parsed.userId) || parsed.userId < 0))
    || (parsed.revision != null && typeof parsed.revision !== 'string')
  ) {
    return null
  }

  return {
    orderId: parsed.orderId,
    userId: parsed.userId || 0,
    revision: parsed.revision || '',
    amount: parsed.amount,
    qrCode: parsed.qrCode,
    expiresAt: parsed.expiresAt,
    paymentType: parsed.paymentType,
    payUrl: parsed.payUrl,
    outTradeNo: parsed.outTradeNo || '',
    clientSecret: parsed.clientSecret,
    payAmount: parsed.payAmount,
    orderType: parsed.orderType === 'subscription' ? 'subscription' : 'balance',
    paymentMode: parsed.paymentMode,
    resumeToken: parsed.resumeToken,
    createdAt: parsed.createdAt,
  }
}

function parseRecoveryEntries(raw: string | null | undefined): PaymentRecoverySnapshot[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as Partial<PaymentRecoveryEnvelope> | Partial<PaymentRecoverySnapshot>
    if (parsed && 'version' in parsed && parsed.version === 2 && Array.isArray(parsed.entries)) {
      return parsed.entries
        .map(entry => normalizeRecoverySnapshot(entry))
        .filter((entry): entry is PaymentRecoverySnapshot => entry !== null)
    }
    const legacy = normalizeRecoverySnapshot(parsed as Partial<PaymentRecoverySnapshot>)
    return legacy ? [legacy] : []
  } catch {
    return []
  }
}

function isRecoveryRetained(snapshot: PaymentRecoverySnapshot, now: number): boolean {
  return snapshot.createdAt > now || now - snapshot.createdAt <= PAYMENT_RECOVERY_RETENTION_MS
}

function writeRecoveryEntries(storage: RecoveryStorage, entries: PaymentRecoverySnapshot[], key: string): void {
  if (entries.length === 0) {
    storage.removeItem(key)
    return
  }
  const envelope: PaymentRecoveryEnvelope = { version: 2, entries }
  storage.setItem(key, JSON.stringify(envelope))
}

export function writePaymentRecoverySnapshot(
  storage: RecoveryStorage,
  snapshot: PaymentRecoverySnapshot,
  key = PAYMENT_RECOVERY_STORAGE_KEY,
): void {
  const normalized = normalizeRecoverySnapshot(snapshot)
  if (!normalized) return
  const now = Date.now()
  const entryKey = recoveryEntryKey(normalized)
  const entries = parseRecoveryEntries(storage.getItem(key))
    .filter(entry => isRecoveryRetained(entry, now) && recoveryEntryKey(entry) !== entryKey)
  entries.push(normalized)
  entries.sort((left, right) => right.createdAt - left.createdAt)
  writeRecoveryEntries(storage, entries.slice(0, 12), key)
}

export function clearPaymentRecoverySnapshot(
  storage: RecoveryStorage,
  expected?: PaymentRecoverySelector | string,
  key = PAYMENT_RECOVERY_STORAGE_KEY,
): boolean {
  // Compatibility for older callers while all financial paths migrate to the
  // compare-and-clear selector.
  if (typeof expected === 'string') {
    storage.removeItem(expected)
    return true
  }
  if (!expected) {
    storage.removeItem(key)
    return true
  }

  const entries = parseRecoveryEntries(storage.getItem(key))
  const expectedKey = recoveryEntryKey(expected)
  const remaining = entries.filter(entry => recoveryEntryKey(entry) !== expectedKey)
  if (remaining.length === entries.length) return false
  writeRecoveryEntries(storage, remaining, key)
  return true
}

export function clearPaymentRecoveryForUser(
  storage: RecoveryStorage,
  userId: number,
  key = PAYMENT_RECOVERY_STORAGE_KEY,
): number {
  if (!Number.isSafeInteger(userId) || userId <= 0) return 0
  const entries = parseRecoveryEntries(storage.getItem(key))
  const remaining = entries.filter(entry => entry.userId !== userId)
  const removed = entries.length - remaining.length
  if (removed > 0) writeRecoveryEntries(storage, remaining, key)
  return removed
}

export function readPaymentRecoverySnapshot(
  raw: string | null | undefined,
  options: {
    now?: number
    resumeToken?: string
    userId?: number
    orderId?: number
    revision?: string
    outTradeNo?: string
    includeExpired?: boolean
  } = {},
): PaymentRecoverySnapshot | null {
  const now = options.now ?? Date.now()
  const entries = parseRecoveryEntries(raw)
    .filter(entry => options.includeExpired || isRecoveryRetained(entry, now))
    .filter(entry => options.userId == null || entry.userId === options.userId)
    .filter(entry => options.orderId == null || entry.orderId === options.orderId)
    .filter(entry => !options.revision || entry.revision === options.revision)
    .filter(entry => !options.resumeToken || entry.resumeToken === options.resumeToken)
    .filter(entry => !options.outTradeNo || entry.outTradeNo === options.outTradeNo)
    .sort((left, right) => right.createdAt - left.createdAt)
  return entries[0] ?? null
}
