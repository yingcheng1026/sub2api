<template>
  <AppLayout>
    <div class="mx-auto flex max-w-md flex-col items-center space-y-6 py-8">
      <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
        {{ invalidSession
          ? t('payment.qr.invalidSession')
          : fulfillmentPending
          ? (fulfillmentFailed ? t('payment.result.fulfillmentFailed') : t('payment.result.processing'))
          : (qrUrl ? scanTitle : t('payment.qr.payInNewWindow')) }}
      </h2>
      <div v-if="qrUrl && !fulfillmentPending" class="rounded-2xl bg-white p-6 shadow-lg dark:bg-dark-800">
        <canvas ref="qrCanvas" class="mx-auto"></canvas>
      </div>
      <!-- Scan prompt for QR code -->
      <p v-if="qrUrl && !expired && !fulfillmentPending && scanHint" class="text-center text-sm text-gray-500 dark:text-gray-400">
        {{ scanHint }}
      </p>
      <div v-if="invalidSession" class="space-y-4 text-center">
        <p class="text-sm text-amber-600 dark:text-amber-400">{{ t('payment.qr.invalidSessionDesc') }}</p>
        <button class="btn btn-primary w-full" @click="router.push('/purchase')">{{ t('payment.result.backToRecharge') }}</button>
      </div>
      <div v-else-if="fulfillmentPending" class="flex flex-col items-center space-y-3 text-center">
        <div
          v-if="fulfillmentFailed"
          class="flex h-16 w-16 items-center justify-center rounded-full bg-amber-100 text-3xl text-amber-500 dark:bg-amber-900/30"
        >
          !
        </div>
        <div v-else class="h-10 w-10 animate-spin rounded-full border-4 border-primary-500 border-t-transparent"></div>
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ fulfillmentFailed ? t('payment.result.fulfillmentFailedHint') : t('payment.result.processingHint') }}
        </p>
        <p class="text-xs text-gray-400 dark:text-gray-500">
          {{ t('payment.orders.orderId') }} #{{ orderId }}
        </p>
      </div>
      <div v-else-if="expired" class="text-center">
        <p class="text-lg font-medium text-red-500">{{ t('payment.qr.expired') }}</p>
        <button class="btn btn-primary mt-4" @click="router.push('/purchase')">{{ t('payment.result.backToRecharge') }}</button>
      </div>
      <div v-else class="text-center">
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ qrUrl ? t('payment.qr.expiresIn') : t('payment.qr.payInNewWindowHint') }}</p>
        <p class="mt-1 text-2xl font-bold tabular-nums text-gray-900 dark:text-white">{{ countdownDisplay }}</p>
        <p class="mt-2 text-sm text-gray-400 dark:text-gray-500">{{ t('payment.qr.waitingPayment') }}</p>
      </div>
      <a v-if="payUrl && !qrUrl && !expired && !fulfillmentPending && !invalidSession" :href="payUrl" target="_blank" rel="noopener noreferrer"
        class="btn btn-primary w-full py-3">
        {{ t('payment.qr.openPayWindow') }}
      </a>
      <!-- Cancel button -->
      <button v-if="!expired && !fulfillmentPending && !invalidSession && orderId" class="btn btn-secondary w-full" :disabled="cancelling" @click="handleCancel">
        {{ cancelling ? t('common.processing') : t('payment.qr.cancelOrder') }}
      </button>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import { usePaymentStore } from '@/stores/payment'
import { useAuthStore } from '@/stores/auth'
import { paymentAPI } from '@/api/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores'
import {
  isPaymentCompleted,
  isPaymentFulfillmentFailed,
  isPaymentFulfillmentPending,
  isPaymentLateSettlementRecoverable,
  isPaymentTerminalFailure,
  PAYMENT_RECOVERY_SESSION_STORAGE_KEY,
  readPaymentRecoverySnapshot,
} from '@/components/payment/paymentFlow'
import { sanitizeUrl } from '@/utils/url'
import QRCode from 'qrcode'
import alipayIcon from '@/assets/icons/alipay.svg'
import wxpayIcon from '@/assets/icons/wxpay.svg'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const paymentStore = usePaymentStore()
const authStore = useAuthStore()
const appStore = useAppStore()

const qrCanvas = ref<HTMLCanvasElement | null>(null)
const qrUrl = ref('')
const payUrl = ref('')
const orderId = ref(0)
const remainingSeconds = ref(0)
const expired = ref(false)
const cancelling = ref(false)
const paymentType = ref('')
const paymentExpiresAt = ref('')
const invalidSession = ref(false)
const latestOrderStatus = ref<string | null>(null)
const latestOrderPaidAt = ref<string | null>(null)

let pollTimer: ReturnType<typeof setInterval> | null = null
let countdownTimer: ReturnType<typeof setInterval> | null = null
let pollRequest: Promise<string | null> | null = null

const countdownDisplay = computed(() => {
  const m = Math.floor(remainingSeconds.value / 60)
  const s = remainingSeconds.value % 60
  return m.toString().padStart(2, '0') + ':' + s.toString().padStart(2, '0')
})

const isAlipay = computed(() => paymentType.value.includes('alipay'))
const isWxpay = computed(() => paymentType.value.includes('wxpay'))
const fulfillmentPending = computed(() => (
  isPaymentFulfillmentPending(latestOrderStatus.value, latestOrderPaidAt.value)
  || isPaymentLateSettlementRecoverable(
    latestOrderStatus.value,
    latestOrderPaidAt.value,
    paymentExpiresAt.value,
  )
))
const fulfillmentFailed = computed(() => isPaymentFulfillmentFailed(
  latestOrderStatus.value,
  latestOrderPaidAt.value
))

const scanTitle = computed(() => {
  if (isAlipay.value) return t('payment.qr.scanAlipay')
  if (isWxpay.value) return t('payment.qr.scanWxpay')
  return t('payment.qr.scanToPay')
})

const scanHint = computed(() => {
  if (isAlipay.value) return t('payment.qr.scanAlipayHint')
  if (isWxpay.value) return t('payment.qr.scanWxpayHint')
  return ''
})

function getLogoForType(): string | null {
  if (isAlipay.value) return alipayIcon
  if (isWxpay.value) return wxpayIcon
  return null
}

async function renderQR() {
  await nextTick()
  if (!qrCanvas.value || !qrUrl.value) return

  // Use medium error correction to support logo overlay while keeping QR code scannable
  const logoSrc = getLogoForType()
  await QRCode.toCanvas(qrCanvas.value, qrUrl.value, {
    width: 256,
    margin: 2,
    errorCorrectionLevel: logoSrc ? 'M' : 'L',
  })

  if (!logoSrc) return

  // Draw logo in center of QR code
  const canvas = qrCanvas.value
  const ctx = canvas.getContext('2d')
  if (!ctx) return

  const img = new Image()
  img.src = logoSrc
  img.onload = () => {
    const logoSize = 48
    const x = (canvas.width - logoSize) / 2
    const y = (canvas.height - logoSize) / 2
    // White background with rounded corners
    const pad = 5
    ctx.fillStyle = '#FFFFFF'
    ctx.beginPath()
    const r = 6
    ctx.moveTo(x - pad + r, y - pad)
    ctx.arcTo(x + logoSize + pad, y - pad, x + logoSize + pad, y + logoSize + pad, r)
    ctx.arcTo(x + logoSize + pad, y + logoSize + pad, x - pad, y + logoSize + pad, r)
    ctx.arcTo(x - pad, y + logoSize + pad, x - pad, y - pad, r)
    ctx.arcTo(x - pad, y - pad, x + logoSize + pad, y - pad, r)
    ctx.fill()
    // Draw logo
    ctx.drawImage(img, x, y, logoSize, logoSize)
  }
}

async function pollStatus(): Promise<string | null> {
  if (!orderId.value) return null
  if (pollRequest) return pollRequest

  pollRequest = (async () => {
    try {
      const order = await paymentStore.pollOrderStatus(orderId.value)
      if (!order) return null
      latestOrderStatus.value = order.status
      latestOrderPaidAt.value = order.paid_at || null
      if (isPaymentCompleted(order.status)) {
        cleanup()
        await router.push({ path: '/payment/result', query: { order_id: String(orderId.value), status: 'success' } })
      } else if (
        isPaymentTerminalFailure(order.status, order.paid_at)
        && !isPaymentLateSettlementRecoverable(
          order.status,
          order.paid_at,
          paymentExpiresAt.value,
        )
      ) {
        cleanup()
        expired.value = true
      }
      return order.status
    } catch {
      return null
    }
  })()

  try {
    return await pollRequest
  } finally {
    pollRequest = null
  }
}

async function handleCountdownDeadline() {
  clearCountdownTimer()
  await pollStatus()
}

function startCountdown(seconds: number) {
  remainingSeconds.value = Math.max(0, seconds)
  if (remainingSeconds.value <= 0) {
    void handleCountdownDeadline()
    return
  }
  countdownTimer = setInterval(() => {
    remainingSeconds.value = Math.max(0, remainingSeconds.value - 1)
    if (remainingSeconds.value <= 0) {
      void handleCountdownDeadline()
    }
  }, 1000)
}

async function handleCancel() {
  if (!orderId.value || cancelling.value) return
  cancelling.value = true
  try {
    const response = await paymentAPI.cancelOrder(orderId.value)
    if (response.data.message === 'already_paid') {
      await pollStatus()
      return
    }
    cleanup()
    router.push('/purchase')
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    cancelling.value = false
  }
}

function cleanup() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  clearCountdownTimer()
}

function clearCountdownTimer() {
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null }
}

watch(qrUrl, () => renderQR())

onMounted(() => {
  const requestedOrderId = Number(route.query.order_id) || 0
  const currentUserId = authStore.user?.id || 0
  const recovery = readPaymentRecoverySnapshot(
    window.sessionStorage.getItem(PAYMENT_RECOVERY_SESSION_STORAGE_KEY),
    {
      orderId: requestedOrderId || undefined,
      userId: currentUserId || undefined,
    },
  )
  if (!requestedOrderId || !recovery || recovery.orderId !== requestedOrderId) {
    invalidSession.value = true
    return
  }

  orderId.value = recovery.orderId
  qrUrl.value = recovery.qrCode
  payUrl.value = sanitizeUrl(recovery.payUrl, { httpsOnly: true })
  paymentType.value = recovery.paymentType
  paymentExpiresAt.value = recovery.expiresAt

  // Calculate countdown from expiresAt
  const expiresAtStr = paymentExpiresAt.value
  let seconds = 30 * 60 // fallback: 30 minutes
  if (expiresAtStr) {
    const expiresAt = new Date(expiresAtStr)
    const now = new Date()
    seconds = Math.floor((expiresAt.getTime() - now.getTime()) / 1000)
  }
  startCountdown(seconds)
  pollTimer = setInterval(pollStatus, 3000)
  renderQR()
})

onUnmounted(() => cleanup())
</script>
