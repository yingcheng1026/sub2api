import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const routeUpdateState = vi.hoisted(() => ({
  handler: null as null | ((to: { query: Record<string, unknown> }) => void),
}))

const routerPush = vi.hoisted(() => vi.fn())
const routerReplace = vi.hoisted(() => vi.fn())
const pollOrderStatus = vi.hoisted(() => vi.fn())
const verifyOrderPublic = vi.hoisted(() => vi.fn())
const resolveOrderPublicByResumeToken = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: routerPush, replace: routerReplace }),
    onBeforeRouteUpdate: (
      handler: (to: { query: Record<string, unknown> }) => void,
    ) => {
      routeUpdateState.handler = handler
    },
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({
    pollOrderStatus,
  }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    verifyOrderPublic,
    resolveOrderPublicByResumeToken,
  },
}))

import PaymentResultView from '../PaymentResultView.vue'
import {
  PAYMENT_RECOVERY_SESSION_STORAGE_KEY,
  PAYMENT_RECOVERY_STORAGE_KEY,
} from '@/components/payment/paymentFlow'

const orderFactory = (status: string) => ({
  id: 42,
  user_id: 9,
  amount: 88,
  pay_amount: 88,
  fee_rate: 0,
  payment_type: 'alipay',
  out_trade_no: 'sub2_20260420abcd1234',
  status,
  order_type: 'balance',
  created_at: '2026-04-20T12:00:00Z',
  expires_at: '2026-04-20T12:30:00Z',
  refund_amount: 0,
  paid_at: ['PAID', 'RECHARGING', 'COMPLETED', 'FAILED'].includes(status)
    ? '2026-04-20T12:00:01Z'
    : undefined,
})

const recoverySnapshotFactory = (resumeToken: string) => ({
  orderId: 42,
  amount: 88,
  qrCode: '',
  expiresAt: '2099-01-01T00:10:00.000Z',
  paymentType: 'alipay',
  payUrl: 'https://pay.example.com/session/42',
  outTradeNo: 'sub2_20260420abcd1234',
  clientSecret: '',
  payAmount: 88,
  orderType: 'balance',
  paymentMode: 'popup',
  resumeToken,
  createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
})

describe('PaymentResultView', () => {
  beforeEach(() => {
    routeState.query = {}
    routerPush.mockReset()
    routerReplace.mockReset()
    pollOrderStatus.mockReset()
    verifyOrderPublic.mockReset()
    resolveOrderPublicByResumeToken.mockReset()
    routeUpdateState.handler = null
    window.localStorage.clear()
    window.sessionStorage.clear()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('captures and removes resume_token before calling the public resolver', async () => {
    routeState.query = {
      resume_token: 'resume-url-secret',
      order_id: '42',
      status: 'success',
    }
    routerReplace.mockResolvedValue(undefined)
    resolveOrderPublicByResumeToken.mockImplementation(async () => {
      expect(routerReplace).toHaveBeenCalledWith({
        query: {
          order_id: '42',
          status: 'success',
        },
      })
      return { data: orderFactory('COMPLETED') }
    })

    mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('resume-url-secret')
    expect(routerReplace).toHaveBeenCalledTimes(1)
  })

  it('recovers the capability from same-origin session storage after a provider redirect', async () => {
    routeState.query = {
      order_id: '42',
      out_trade_no: 'sub2_20260420abcd1234',
      status: 'success',
    }
    window.sessionStorage.setItem(
      PAYMENT_RECOVERY_SESSION_STORAGE_KEY,
      JSON.stringify(recoverySnapshotFactory('session-only-resume-token')),
    )
    resolveOrderPublicByResumeToken.mockResolvedValue({
      data: orderFactory('COMPLETED'),
    })

    const wrapper = mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('session-only-resume-token')
    expect(routerReplace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.success')
    expect(window.sessionStorage.getItem(PAYMENT_RECOVERY_SESSION_STORAGE_KEY)).toBeNull()
  })

  it('renders a pending state instead of a failure state when the restored order is still pending', async () => {
    routeState.query = {
      resume_token: 'resume-42',
      order_id: '999',
      status: 'success',
    }
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 42,
      amount: 88,
      qrCode: '',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: 'https://pay.example.com/session/42',
      outTradeNo: 'sub2_20260420abcd1234',
      clientSecret: '',
      payAmount: 88,
      orderType: 'balance',
      paymentMode: 'redirect',
      resumeToken: 'resume-42',
      createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
    }))
    resolveOrderPublicByResumeToken.mockResolvedValue({
      data: orderFactory('PENDING'),
    })

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('resume-42')
    expect(pollOrderStatus).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.processing')
    expect(wrapper.text()).not.toContain('payment.result.success')
    expect(wrapper.text()).not.toContain('payment.result.failed')
  })

  it('prefers the public resume-token result over a stale restored DB snapshot', async () => {
    routeState.query = {
      resume_token: 'resume-authoritative',
      order_id: '42',
      status: 'success',
    }
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 42,
      amount: 88,
      qrCode: '',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: 'https://pay.example.com/session/42',
      outTradeNo: 'sub2_20260420abcd1234',
      clientSecret: '',
      payAmount: 88,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: 'resume-authoritative',
      createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
    }))
    resolveOrderPublicByResumeToken.mockResolvedValue({
      data: {
        ...orderFactory('COMPLETED'),
        amount: 100,
        pay_amount: 103,
        fee_rate: 3,
      },
    })

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(pollOrderStatus).not.toHaveBeenCalled()
    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('resume-authoritative')
    expect(wrapper.text()).toContain('payment.result.success')
    expect(wrapper.text()).toContain('103.00')
    expect(wrapper.text()).toContain('100.00')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('keeps refreshing through PAID and RECHARGING until fulfillment is COMPLETED', async () => {
    vi.useFakeTimers()
    routeState.query = {
      resume_token: 'resume-77',
    }
    window.localStorage.setItem(
      PAYMENT_RECOVERY_STORAGE_KEY,
      JSON.stringify(recoverySnapshotFactory('resume-77')),
    )
    resolveOrderPublicByResumeToken
      .mockResolvedValueOnce({
        data: orderFactory('PENDING'),
      })
      .mockResolvedValueOnce({
        data: orderFactory('PAID'),
      })
      .mockResolvedValueOnce({
        data: orderFactory('RECHARGING'),
      })
      .mockResolvedValueOnce({
        data: orderFactory('COMPLETED'),
      })

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('payment.result.processing')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).not.toBeNull()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('payment.result.processing')
    expect(wrapper.text()).not.toContain('payment.result.failed')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).not.toBeNull()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).toContain('payment.result.processing')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).not.toBeNull()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(4)
    expect(wrapper.text()).toContain('payment.result.success')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('preserves recovery and keeps refreshing when paid fulfillment is FAILED', async () => {
    vi.useFakeTimers()
    routeState.query = { resume_token: 'resume-failed-fulfillment' }
    window.localStorage.setItem(
      PAYMENT_RECOVERY_STORAGE_KEY,
      JSON.stringify(recoverySnapshotFactory('resume-failed-fulfillment')),
    )
    resolveOrderPublicByResumeToken
      .mockResolvedValueOnce({ data: orderFactory('FAILED') })
      .mockResolvedValueOnce({ data: orderFactory('COMPLETED') })

    const wrapper = mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.fulfillmentFailed')
    expect(wrapper.text()).toContain('payment.result.fulfillmentFailedHint')
    expect(wrapper.text()).not.toContain('payment.result.failed')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).not.toBeNull()

    await vi.advanceTimersByTimeAsync(10000)
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('payment.result.success')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('falls back to order_id polling when resume-token recovery fails', async () => {
    routeState.query = {
      resume_token: 'resume-fail',
      order_id: '77',
    }
    window.localStorage.setItem(
      PAYMENT_RECOVERY_STORAGE_KEY,
      JSON.stringify({
        ...recoverySnapshotFactory('resume-fail'),
        orderId: 42,
      }),
    )
    resolveOrderPublicByResumeToken.mockRejectedValueOnce(new Error('resume failed'))
    pollOrderStatus.mockResolvedValueOnce({
      ...orderFactory('COMPLETED'),
      id: 77,
    })

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('resume-fail')
    expect(pollOrderStatus).toHaveBeenCalledWith(77)
    expect(verifyOrderPublic).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.success')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('does not fall back to unsigned out_trade_no when resume-token recovery fails', async () => {
    routeState.query = {
      resume_token: 'resume-fail',
      out_trade_no: 'legacy-should-not-run',
      trade_status: 'TRADE_SUCCESS',
    }
    resolveOrderPublicByResumeToken.mockRejectedValueOnce(new Error('resume failed'))

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('resume-fail')
    expect(verifyOrderPublic).not.toHaveBeenCalled()
    expect(pollOrderStatus).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.failed')
    expect(wrapper.text()).toContain('legacy-should-not-run')
  })

  it('ignores a stale global recovery snapshot when legacy return markers do not identify the order', async () => {
    routeState.query = {
      trade_status: 'TRADE_SUCCESS',
    }
    window.localStorage.setItem(
      PAYMENT_RECOVERY_STORAGE_KEY,
      JSON.stringify(recoverySnapshotFactory('resume-stale')),
    )

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).not.toHaveBeenCalled()
    expect(verifyOrderPublic).not.toHaveBeenCalled()
    expect(pollOrderStatus).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.failed')
    expect(wrapper.text()).not.toContain('sub2_20260420abcd1234')
  })

  it('shows provider return details without querying by unsigned out_trade_no', async () => {
    routeState.query = {
      out_trade_no: 'legacy-123',
      trade_status: 'TRADE_SUCCESS',
    }
    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(verifyOrderPublic).not.toHaveBeenCalled()
    expect(pollOrderStatus).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.failed')
    expect(wrapper.text()).toContain('legacy-123')
  })

  it('does not use public out_trade_no verification for bare order numbers without legacy return markers', async () => {
    routeState.query = {
      out_trade_no: 'legacy-bare',
    }

    mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(verifyOrderPublic).not.toHaveBeenCalled()
  })

  it('resolves order by resume token when local recovery snapshot is missing', async () => {
    routeState.query = {
      resume_token: 'resume-77',
    }
    resolveOrderPublicByResumeToken.mockResolvedValue({
      data: orderFactory('COMPLETED'),
    })

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledWith('resume-77')
    expect(wrapper.text()).toContain('payment.result.success')
  })

  it('normalizes aliased payment methods before rendering the label', async () => {
    routeState.query = {
      resume_token: 'resume-88',
    }
    resolveOrderPublicByResumeToken.mockResolvedValueOnce({
      data: {
        ...orderFactory('COMPLETED'),
        payment_type: 'alipay_direct',
      },
    })

    const wrapper = mount(PaymentResultView, {
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('payment.methods.alipay')
    expect(wrapper.text()).not.toContain('payment.methods.alipay_direct')
  })

  it('uses an expired local snapshot to verify PAID with the backend before clearing it', async () => {
    vi.useFakeTimers()
    routeState.query = { resume_token: 'resume-expired-local' }
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      ...recoverySnapshotFactory('resume-expired-local'),
      expiresAt: '2024-01-01T00:10:00.000Z',
      createdAt: Date.UTC(2024, 0, 1, 0, 0, 0),
    }))
    resolveOrderPublicByResumeToken.mockRejectedValue(new Error('public lookup unavailable'))
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PAID'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledWith(42)
    expect(wrapper.text()).toContain('payment.result.processing')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).not.toBeNull()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('payment.result.success')
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('bounds pending public status polling instead of retrying forever', async () => {
    vi.useFakeTimers()
    routeState.query = { resume_token: 'resume-bounded-polling' }
    resolveOrderPublicByResumeToken.mockResolvedValue({ data: orderFactory('PENDING') })

    const wrapper = mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()

    await vi.runAllTimersAsync()
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(16)
    expect(wrapper.text()).toContain('payment.result.processing')
    wrapper.unmount()
  })

  it('ignores an in-flight refresh after unmount and preserves a newer recovery entry', async () => {
    vi.useFakeTimers()
    routeState.query = { resume_token: 'resume-old' }
    window.localStorage.setItem(
      PAYMENT_RECOVERY_STORAGE_KEY,
      JSON.stringify(recoverySnapshotFactory('resume-old')),
    )

    let resolveLateRefresh: ((value: { data: ReturnType<typeof orderFactory> }) => void) | undefined
    resolveOrderPublicByResumeToken
      .mockResolvedValueOnce({ data: orderFactory('PENDING') })
      .mockImplementationOnce(() => new Promise(resolve => {
        resolveLateRefresh = resolve
      }))

    const wrapper = mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()

    await vi.advanceTimersByTimeAsync(2000)
    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    const newerRecovery = {
      ...recoverySnapshotFactory('resume-new'),
      orderId: 99,
      outTradeNo: 'sub2_newer_order',
    }
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify(newerRecovery))

    resolveLateRefresh?.({ data: orderFactory('COMPLETED') })
    await flushPromises()

    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toContain('resume-new')
    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(2)
  })

  it('invalidates an in-flight refresh when the result route is reused for another order', async () => {
    vi.useFakeTimers()
    routeState.query = { resume_token: 'resume-old' }
    window.localStorage.setItem(
      PAYMENT_RECOVERY_STORAGE_KEY,
      JSON.stringify(recoverySnapshotFactory('resume-old')),
    )

    let resolveLateRefresh: ((value: { data: ReturnType<typeof orderFactory> }) => void) | undefined
    resolveOrderPublicByResumeToken
      .mockResolvedValueOnce({ data: orderFactory('PENDING') })
      .mockImplementationOnce(() => new Promise(resolve => {
        resolveLateRefresh = resolve
      }))
      .mockResolvedValueOnce({
        data: {
          ...orderFactory('COMPLETED'),
          id: 99,
          out_trade_no: 'sub2_newer_order',
        },
      })

    const wrapper = mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)

    routeState.query = { resume_token: 'resume-new' }
    routeUpdateState.handler?.({ query: routeState.query })
    await flushPromises()

    resolveLateRefresh?.({ data: orderFactory('COMPLETED') })
    await flushPromises()

    expect(resolveOrderPublicByResumeToken).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).toContain('sub2_newer_order')
  })

  it('clears only the terminal order recovery entry', async () => {
    routeState.query = { resume_token: 'resume-old' }
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      version: 2,
      entries: [
        {
          ...recoverySnapshotFactory('resume-old'),
          userId: 9,
          revision: 'old-revision',
        },
        {
          ...recoverySnapshotFactory('resume-new'),
          userId: 9,
          orderId: 99,
          outTradeNo: 'sub2_newer_order',
          revision: 'new-revision',
        },
      ],
    }))
    resolveOrderPublicByResumeToken.mockResolvedValueOnce({
      data: orderFactory('COMPLETED'),
    })

    mount(PaymentResultView, {
      global: { stubs: { OrderStatusBadge: true } },
    })
    await flushPromises()

    const stored = window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)
    expect(stored).toContain('resume-new')
    expect(stored).not.toContain('resume-old')
  })
})
