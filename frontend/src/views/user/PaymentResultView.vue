<template>
  <div class="flex min-h-screen items-center justify-center bg-gray-50 px-4 dark:bg-dark-900">
    <div class="w-full max-w-md space-y-6">
      <!-- Loading -->
      <div v-if="loading" class="flex items-center justify-center py-20">
        <div class="h-8 w-8 animate-spin rounded-full border-4 border-primary-500 border-t-transparent"></div>
      </div>
      <template v-else>
        <!-- Status Icon -->
        <div class="text-center">
          <div v-if="isSuccess"
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30">
            <svg class="h-10 w-10 text-green-500" fill="none" viewBox="0 0 24 24" stroke="currentColor"
              stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
            </svg>
          </div>
          <div v-else-if="isPending"
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-yellow-100 dark:bg-yellow-900/30">
            <div class="h-10 w-10 animate-spin rounded-full border-4 border-yellow-500 border-t-transparent"></div>
          </div>
          <div v-else-if="isFulfillmentFailed"
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-amber-100 dark:bg-amber-900/30">
            <svg class="h-10 w-10 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v4m0 4h.01M10.3 3.7 2.6 17a2 2 0 0 0 1.7 3h15.4a2 2 0 0 0 1.7-3L13.7 3.7a2 2 0 0 0-3.4 0Z" />
            </svg>
          </div>
          <div v-else
            class="mx-auto flex h-20 w-20 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/30">
            <svg class="h-10 w-10 text-red-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </div>
          <h2 class="mt-4 text-2xl font-bold text-gray-900 dark:text-white">
            {{ statusTitle }}
          </h2>
          <p v-if="isPending" class="mt-2 text-sm text-gray-500 dark:text-gray-400">
            {{ t('payment.result.processingHint') }}
          </p>
          <p v-else-if="isFulfillmentFailed" class="mt-2 text-sm text-gray-500 dark:text-gray-400">
            {{ t('payment.result.fulfillmentFailedHint') }}
          </p>
        </div>
        <!-- Order Info -->
        <div v-if="order" class="rounded-xl bg-white p-5 shadow-sm dark:bg-dark-800">
          <div class="space-y-3 text-sm">
            <div class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.orderId') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">#{{ order.id }}</span>
            </div>
            <div v-if="order.out_trade_no" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.orderNo') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{ order.out_trade_no }}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.baseAmount') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">&#165;{{ baseAmount.toFixed(2) }}</span>
            </div>
            <div v-if="order.fee_rate > 0" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.fee') }} ({{ order.fee_rate }}%)</span>
              <span class="font-medium text-gray-900 dark:text-white">&#165;{{ feeAmount.toFixed(2) }}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.payAmount') }}</span>
              <span class="font-bold text-primary-600 dark:text-primary-400">&#165;{{ order.pay_amount.toFixed(2) }}</span>
            </div>
            <div v-if="order.amount !== order.pay_amount" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.creditedAmount') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{ order.order_type === 'balance' ? '$' : '¥' }}{{ order.amount.toFixed(2) }}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.paymentMethod') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{ t(paymentMethodI18nKey(order.payment_type), normalizedOrderPaymentType(order.payment_type)) }}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.status') }}</span>
              <OrderStatusBadge :status="order.status" />
            </div>
          </div>
        </div>
        <!-- EasyPay return info (when no order loaded) -->
        <div v-else-if="returnInfo" class="rounded-xl bg-white p-5 shadow-sm dark:bg-dark-800">
          <div class="space-y-3 text-sm">
            <div v-if="returnInfo.outTradeNo" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.orderId') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{ returnInfo.outTradeNo }}</span>
            </div>
            <div v-if="returnInfo.money" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.payAmount') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">&#165;{{ returnInfo.money }}</span>
            </div>
            <div v-if="returnInfo.type" class="flex justify-between">
              <span class="text-gray-500 dark:text-gray-400">{{ t('payment.orders.paymentMethod') }}</span>
              <span class="font-medium text-gray-900 dark:text-white">{{ t(paymentMethodI18nKey(returnInfo.type), normalizedOrderPaymentType(returnInfo.type)) }}</span>
            </div>
          </div>
        </div>
        <!-- Actions -->
        <div class="flex gap-3">
          <button class="btn btn-secondary flex-1" @click="router.push('/purchase')">{{ t('payment.result.backToRecharge') }}</button>
          <button class="btn btn-primary flex-1" @click="router.push('/orders')">{{ t('payment.result.viewOrders') }}</button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onBeforeUnmount, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import type { LocationQuery } from 'vue-router'
import OrderStatusBadge from '@/components/payment/OrderStatusBadge.vue'
import {
  PAYMENT_RECOVERY_SESSION_STORAGE_KEY,
  PAYMENT_RECOVERY_STORAGE_KEY,
  clearPaymentRecoverySnapshot,
  isPaymentCompleted,
  isPaymentFulfillmentFailed,
  isPaymentLateSettlementRecoverable,
  isPaymentStillProcessing,
  readPaymentRecoverySnapshot,
} from '@/components/payment/paymentFlow'
import type {
  PaymentRecoverySelector,
  PaymentRecoverySnapshot,
} from '@/components/payment/paymentFlow'
import { usePaymentStore } from '@/stores/payment'
import { paymentAPI } from '@/api/payment'
import type { PaymentOrder } from '@/types/payment'
import { normalizePaymentMethodForDisplay, paymentMethodI18nKey } from './paymentUx'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const paymentStore = usePaymentStore()

const order = ref<PaymentOrder | null>(null)
const loading = ref(true)

interface ReturnInfo {
  outTradeNo: string
  money: string
  type: string
  tradeStatus: string
}
const returnInfo = ref<ReturnInfo | null>(null)

const STATUS_REFRESH_INTERVAL_MS = 2000
const FAILED_STATUS_REFRESH_INTERVAL_MS = 10000
const STATUS_REFRESH_MAX_INTERVAL_MS = 10000
const STATUS_REFRESH_BACKOFF_AFTER = 3
const STATUS_REFRESH_MAX_ATTEMPTS = 15

let statusRefreshTimer: ReturnType<typeof setTimeout> | null = null
let statusRefreshAttempts = 0
let resultGeneration = 0
let viewDisposed = false
let pendingSanitizedQueryKey: string | null = null

/** 充值金额 = pay_amount / (1 + fee_rate/100)，fee_rate=0 时等于 pay_amount */
const baseAmount = computed(() => {
  if (!order.value || order.value.fee_rate <= 0) return order.value?.pay_amount ?? 0
  return Math.round((order.value.pay_amount / (1 + order.value.fee_rate / 100)) * 100) / 100
})

/** 手续费 = pay_amount - baseAmount */
const feeAmount = computed(() => {
  if (!order.value || order.value.fee_rate <= 0) return 0
  return Math.round((order.value.pay_amount - baseAmount.value) * 100) / 100
})

const isSuccess = computed(() => {
  return isSuccessStatus(order.value?.status)
})

const isPending = computed(() => {
  return isPendingStatus(order.value?.status)
    || isPaymentLateSettlementRecoverable(
      order.value?.status,
      order.value?.paid_at,
      order.value?.expires_at,
    )
})

const isFulfillmentFailed = computed(() => {
  return isPaymentFulfillmentFailed(order.value?.status, order.value?.paid_at)
})

const statusTitle = computed(() => {
  if (isSuccess.value) {
    return t('payment.result.success')
  }
  if (isPending.value) {
    return t('payment.result.processing')
  }
  if (isFulfillmentFailed.value) {
    return t('payment.result.fulfillmentFailed')
  }
  return t('payment.result.failed')
})

function normalizedOrderPaymentType(paymentType: string): string {
  return normalizePaymentMethodForDisplay(paymentType) || paymentType
}

function isSuccessStatus(status: string | null | undefined): boolean {
  return isPaymentCompleted(status)
}

function isPendingStatus(status: string | null | undefined): boolean {
  return isPaymentStillProcessing(status)
}

function isRefreshableStatus(
  status: string | null | undefined,
  paidAt?: string | null,
  expiresAt?: string | null,
): boolean {
  return isPendingStatus(status)
    || isPaymentFulfillmentFailed(status, paidAt)
    || isPaymentLateSettlementRecoverable(status, paidAt, expiresAt)
}

function readRouteQueryString(query: LocationQuery, key: string): string {
  const value = query[key]
  if (Array.isArray(value)) {
    return typeof value[0] === 'string' ? value[0] : ''
  }
  return typeof value === 'string' ? value : ''
}

function queryKey(query: LocationQuery): string {
  return JSON.stringify(Object.keys(query).sort().map(key => [key, query[key]]))
}

async function removeResumeTokenFromAddressBar(query: LocationQuery): Promise<void> {
  const sanitizedQuery = { ...query }
  delete sanitizedQuery.resume_token
  pendingSanitizedQueryKey = queryKey(sanitizedQuery)
  try {
    await router.replace({ query: sanitizedQuery })
  } catch (_err: unknown) {
    // Keep the capability out of browser history/referrers even if a router
    // guard unexpectedly rejects the same-route query replacement.
    if (typeof window === 'undefined') return
    const sanitizedURL = new URL(window.location.href)
    sanitizedURL.searchParams.delete('resume_token')
    window.history.replaceState(
      window.history.state,
      '',
      `${sanitizedURL.pathname}${sanitizedURL.search}${sanitizedURL.hash}`,
    )
  } finally {
    pendingSanitizedQueryKey = null
  }
}

function restoreRecoverySnapshot(context: {
  resumeToken: string
  routeOrderId: number
  routeOutTradeNo: string
}) {
  if (typeof window === 'undefined') {
    return null
  }

  const sessionSnapshot = window.sessionStorage.getItem(PAYMENT_RECOVERY_SESSION_STORAGE_KEY)
  const persistentSnapshot = window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)
  const readMatchingSnapshot = (options: Parameters<typeof readPaymentRecoverySnapshot>[1]) => {
    return readPaymentRecoverySnapshot(sessionSnapshot, options)
      ?? readPaymentRecoverySnapshot(persistentSnapshot, options)
  }

  if (context.resumeToken) {
    return readMatchingSnapshot({
      resumeToken: context.resumeToken,
      includeExpired: true,
    })
  }

  if (!context.routeOrderId && !context.routeOutTradeNo) {
    return null
  }

  const restored = readMatchingSnapshot({
    orderId: context.routeOrderId || undefined,
    outTradeNo: context.routeOutTradeNo || undefined,
    includeExpired: true,
  })
  if (!restored) {
    return null
  }

  if (context.routeOrderId > 0 && restored.orderId !== context.routeOrderId) {
    return null
  }

  if (context.routeOutTradeNo && restored.outTradeNo !== context.routeOutTradeNo) {
    return null
  }

  return restored
}

async function resolveOrderFromResumeToken(resumeToken: string): Promise<PaymentOrder | null> {
  try {
    const result = await paymentAPI.resolveOrderPublicByResumeToken(resumeToken)
    return result.data
  } catch (_err: unknown) {
    return null
  }
}

function clearStatusRefreshTimer(): void {
  if (statusRefreshTimer !== null) {
    clearTimeout(statusRefreshTimer)
    statusRefreshTimer = null
  }
}

function recoverySelectorFor(
  snapshot: PaymentRecoverySnapshot | null | undefined,
): PaymentRecoverySelector | null {
  if (!snapshot || !snapshot.orderId) return null
  return {
    userId: snapshot.userId || 0,
    orderId: snapshot.orderId,
    revision: snapshot.revision || '',
  }
}

function clearRecoverySnapshot(selector: PaymentRecoverySelector | null): void {
  if (typeof window === 'undefined' || !selector) return
  clearPaymentRecoverySnapshot(window.localStorage, selector, PAYMENT_RECOVERY_STORAGE_KEY)
  clearPaymentRecoverySnapshot(
    window.sessionStorage,
    selector,
    PAYMENT_RECOVERY_SESSION_STORAGE_KEY,
  )
}

function clearRecoverySnapshotForTerminalStatus(
  paymentOrder: PaymentOrder,
  selector: PaymentRecoverySelector | null,
): void {
  if (!isRefreshableStatus(paymentOrder.status, paymentOrder.paid_at, paymentOrder.expires_at)) {
    clearRecoverySnapshot(selector)
  }
}

function isResultGenerationActive(generation: number): boolean {
  return !viewDisposed && generation === resultGeneration
}

function scheduleStatusRefresh(
  refreshOrder: (() => Promise<PaymentOrder | null>) | null,
  generation: number,
  recoverySelector: PaymentRecoverySelector | null,
): void {
  clearStatusRefreshTimer()
  if (!isResultGenerationActive(generation) || !refreshOrder || !isRefreshableStatus(
    order.value?.status,
    order.value?.paid_at,
    order.value?.expires_at,
  )) {
    return
  }
  if (statusRefreshAttempts >= STATUS_REFRESH_MAX_ATTEMPTS) {
    return
  }

  const isFulfillmentRetry = isPaymentFulfillmentFailed(order.value?.status, order.value?.paid_at)
  const pendingBackoffStep = Math.max(0, statusRefreshAttempts - STATUS_REFRESH_BACKOFF_AFTER + 1)
  const refreshIntervalMs = isFulfillmentRetry
    ? FAILED_STATUS_REFRESH_INTERVAL_MS
    : Math.min(
      STATUS_REFRESH_INTERVAL_MS * (2 ** pendingBackoffStep),
      STATUS_REFRESH_MAX_INTERVAL_MS,
    )
  statusRefreshAttempts += 1
  statusRefreshTimer = setTimeout(async () => {
    statusRefreshTimer = null
    const refreshedOrder = await refreshOrder()
    if (!isResultGenerationActive(generation)) {
      return
    }
    if (refreshedOrder) {
      order.value = refreshedOrder
      clearRecoverySnapshotForTerminalStatus(refreshedOrder, recoverySelector)
    }

    if (isRefreshableStatus(
      order.value?.status,
      order.value?.paid_at,
      order.value?.expires_at,
    )) {
      scheduleStatusRefresh(refreshOrder, generation, recoverySelector)
    }
  }, refreshIntervalMs)
}

async function loadPaymentResult(query: LocationQuery): Promise<void> {
  const generation = ++resultGeneration
  clearStatusRefreshTimer()
  statusRefreshAttempts = 0
  order.value = null
  returnInfo.value = null
  loading.value = true

  const routeResumeToken = readRouteQueryString(query, 'resume_token')
  if (routeResumeToken) {
    await removeResumeTokenFromAddressBar(query)
    if (!isResultGenerationActive(generation)) return
  }
  const routeOrderId = Number(readRouteQueryString(query, 'order_id')) || 0
  let outTradeNo = readRouteQueryString(query, 'out_trade_no')
  let orderId = 0

  const restored = restoreRecoverySnapshot({
    resumeToken: routeResumeToken,
    routeOrderId,
    routeOutTradeNo: outTradeNo,
  })
  const resumeToken = routeResumeToken || restored?.resumeToken || ''
  const recoverySelector = recoverySelectorFor(restored)
  if (restored?.orderId) {
    orderId = restored.orderId
  }
  if (!outTradeNo && restored?.outTradeNo) {
    outTradeNo = restored.outTradeNo
  }

  if (resumeToken) {
    const resolvedOrder = await resolveOrderFromResumeToken(resumeToken)
    if (!isResultGenerationActive(generation)) return
    if (resolvedOrder) {
      order.value = resolvedOrder
      if (!orderId) {
        orderId = resolvedOrder.id
      }
    } else if (routeOrderId > 0) {
      orderId = routeOrderId
    }
  } else if (routeOrderId > 0) {
    orderId = routeOrderId
  }

  const hasLegacyFallbackContext = readRouteQueryString(query, 'trade_status').trim() !== ''

  const restoredMatchesResumeToken = !!resumeToken && restored?.resumeToken === resumeToken
  if (!order.value && orderId && (!resumeToken || routeOrderId > 0 || restoredMatchesResumeToken)) {
    try {
      const polledOrder = await paymentStore.pollOrderStatus(orderId)
      if (!isResultGenerationActive(generation)) return
      order.value = polledOrder
    } catch (_err: unknown) {
      // Authenticated/local order lookup is best-effort on the public result page.
    }
  }

  if (!order.value && !orderId && outTradeNo && hasLegacyFallbackContext) {
    returnInfo.value = {
      outTradeNo,
      money: String(query.money || ''),
      type: String(query.type || ''),
      tradeStatus: String(query.trade_status || ''),
    }
  }

  const refreshOrder = async (): Promise<PaymentOrder | null> => {
    if (resumeToken) {
      const resolvedOrder = await resolveOrderFromResumeToken(resumeToken)
      if (!isResultGenerationActive(generation)) return null
      if (resolvedOrder) {
        return resolvedOrder
      }
    }

    if (orderId) {
      try {
        const polledOrder = await paymentStore.pollOrderStatus(orderId)
        return isResultGenerationActive(generation) ? polledOrder : null
      } catch (_err: unknown) {
        return null
      }
    }

    return null
  }

  if (!isResultGenerationActive(generation)) return
  if (isRefreshableStatus(order.value?.status, order.value?.paid_at, order.value?.expires_at)) {
    scheduleStatusRefresh(refreshOrder, generation, recoverySelector)
  } else if (order.value) {
    clearRecoverySnapshotForTerminalStatus(order.value, recoverySelector)
  } else if (returnInfo.value) {
    clearRecoverySnapshot(recoverySelector)
  }
  loading.value = false
}

onMounted(() => {
  void loadPaymentResult(route.query)
})

onBeforeRouteUpdate((to) => {
  if (pendingSanitizedQueryKey === queryKey(to.query)) {
    pendingSanitizedQueryKey = null
    return
  }
  void loadPaymentResult(to.query)
})

onBeforeUnmount(() => {
  viewDisposed = true
  resultGeneration += 1
  clearStatusRefreshTimer()
})
</script>
