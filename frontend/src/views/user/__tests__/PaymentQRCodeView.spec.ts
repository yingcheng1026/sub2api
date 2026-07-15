import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { PAYMENT_RECOVERY_SESSION_STORAGE_KEY } from '@/components/payment/paymentFlow'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const routerPush = vi.hoisted(() => vi.fn())
const pollOrderStatus = vi.hoisted(() => vi.fn())
const cancelOrder = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: routerPush }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({ pollOrderStatus }),
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 9 } }),
}))

vi.mock('@/api/payment', () => ({ paymentAPI: { cancelOrder } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('qrcode', () => ({ default: { toCanvas: vi.fn() } }))

import PaymentQRCodeView from '../PaymentQRCodeView.vue'

const orderFactory = (status: string) => ({
  id: 42,
  user_id: 9,
  amount: 88,
  pay_amount: 88,
  fee_rate: 0,
  payment_type: 'alipay',
  out_trade_no: 'sub2_deadline_42',
  status,
  order_type: 'balance',
  created_at: '2026-04-20T12:00:00Z',
  expires_at: '2026-04-20T12:00:01Z',
  refund_amount: 0,
  paid_at: ['PAID', 'RECHARGING', 'COMPLETED', 'FAILED'].includes(status)
    ? '2026-04-20T12:00:01Z'
    : undefined,
})

describe('PaymentQRCodeView deadline fulfillment', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-04-20T12:00:00Z'))
    routeState.query = {
      order_id: '42',
      expires_at: '2026-04-20T12:00:01Z',
      payment_type: 'alipay',
    }
    routerPush.mockReset()
    pollOrderStatus.mockReset()
    cancelOrder.mockReset()
    showError.mockReset()
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      beginPath: vi.fn(),
      moveTo: vi.fn(),
      arcTo: vi.fn(),
      fill: vi.fn(),
      drawImage: vi.fn(),
      fillStyle: '',
    } as unknown as CanvasRenderingContext2D)
    window.sessionStorage.clear()
    window.sessionStorage.setItem(PAYMENT_RECOVERY_SESSION_STORAGE_KEY, JSON.stringify({
      orderId: 42,
      userId: 9,
      revision: 'qr-42',
      amount: 88,
      qrCode: 'https://pay.example/qr/42',
      expiresAt: '2026-04-20T12:00:01Z',
      paymentType: 'alipay',
      payUrl: '',
      outTradeNo: 'sub2_deadline_42',
      clientSecret: '',
      payAmount: 88,
      orderType: 'balance',
      paymentMode: 'qrcode',
      resumeToken: '',
      createdAt: Date.now(),
    }))
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('checks at the deadline and keeps polling PAID until COMPLETED', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PAID'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
        },
      },
    })

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')
    expect(routerPush).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(routerPush).toHaveBeenCalledWith({
      path: '/payment/result',
      query: { order_id: '42', status: 'success' },
    })
  })

  it('keeps polling a retryable FAILED fulfillment without showing expiry', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('FAILED'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
        },
      },
    })

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.fulfillmentFailed')
    expect(wrapper.text()).not.toContain('payment.qr.expired')
    expect(routerPush).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(routerPush).toHaveBeenCalledWith({
      path: '/payment/result',
      query: { order_id: '42', status: 'success' },
    })
  })

  it('ignores a query-controlled payment destination and uses the order-bound snapshot', async () => {
    routeState.query = {
      order_id: '42',
      pay_url: 'javascript:alert(document.domain)',
    }
    window.sessionStorage.setItem(PAYMENT_RECOVERY_SESSION_STORAGE_KEY, JSON.stringify({
      orderId: 42,
      userId: 9,
      revision: 'redirect-42',
      amount: 88,
      qrCode: '',
      expiresAt: '2026-04-20T12:30:00Z',
      paymentType: 'alipay',
      payUrl: 'https://payments.example/order/42',
      outTradeNo: 'sub2_redirect_42',
      clientSecret: '',
      payAmount: 88,
      orderType: 'balance',
      paymentMode: 'redirect',
      resumeToken: '',
      createdAt: Date.now(),
    }))

    const wrapper = mount(PaymentQRCodeView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } },
    })
    await flushPromises()

    const link = wrapper.get('a[href]')
    expect(link.attributes('href')).toBe('https://payments.example/order/42')
    expect(wrapper.html()).not.toContain('javascript:')
  })

  it('fails closed when no order-bound session snapshot exists', async () => {
    window.sessionStorage.clear()
    routeState.query = {
      order_id: '42',
      pay_url: 'https://evil.example/pay',
    }

    const wrapper = mount(PaymentQRCodeView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('payment.qr.invalidSession')
    expect(wrapper.find('a[href]').exists()).toBe(false)
    expect(pollOrderStatus).not.toHaveBeenCalled()
  })
})
