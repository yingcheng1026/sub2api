import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { PAYMENT_RECOVERY_SESSION_STORAGE_KEY } from '@/components/payment/paymentFlow'

const routeState = vi.hoisted(() => ({
  query: {
    order_id: '42',
    method: 'wechat_pay',
    resume_token: 'resume-42',
  } as Record<string, unknown>,
}))
const routerPush = vi.hoisted(() => vi.fn())
const routerReplace = vi.hoisted(() => vi.fn())
const pollOrderStatus = vi.hoisted(() => vi.fn())
const fetchConfig = vi.hoisted(() => vi.fn())
const getOrder = vi.hoisted(() => vi.fn())
const confirmWechatPayPayment = vi.hoisted(() => vi.fn())
const loadStripe = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: routerPush, replace: routerReplace }),
  }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({
    config: { stripe_publishable_key: 'pk_test_42' },
    fetchConfig,
    pollOrderStatus,
  }),
}))
vi.mock('@/api/payment', () => ({ paymentAPI: { getOrder } }))
vi.mock('@/utils/device', () => ({ isMobileDevice: () => false }))
vi.mock('@stripe/stripe-js', () => ({ loadStripe }))

import StripePaymentView from '../StripePaymentView.vue'

const orderFactory = (status: string) => ({
  id: 42,
  user_id: 9,
  amount: 88,
  pay_amount: 88,
  fee_rate: 0,
  payment_type: 'stripe',
  out_trade_no: 'sub2_stripe_42',
  status,
  order_type: 'balance',
  created_at: '2026-04-20T12:00:00Z',
  expires_at: '2026-04-20T12:30:00Z',
  refund_amount: 0,
  paid_at: ['PAID', 'RECHARGING', 'COMPLETED', 'FAILED'].includes(status)
    ? '2026-04-20T12:00:01Z'
    : undefined,
})

describe('StripePaymentView fulfillment polling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    routeState.query = {
      order_id: '42',
      method: 'wechat_pay',
      resume_token: 'resume-42',
    }
    routerPush.mockReset()
    routerReplace.mockReset().mockResolvedValue(undefined)
    pollOrderStatus.mockReset()
    fetchConfig.mockReset().mockResolvedValue(undefined)
    getOrder.mockReset().mockResolvedValue({ data: orderFactory('PENDING') })
    confirmWechatPayPayment.mockReset().mockResolvedValue({ paymentIntent: { status: 'succeeded' } })
    loadStripe.mockReset().mockResolvedValue({ confirmWechatPayPayment })
    window.localStorage.clear()
    window.sessionStorage.clear()
    window.sessionStorage.setItem(PAYMENT_RECOVERY_SESSION_STORAGE_KEY, JSON.stringify({
      orderId: 42,
      amount: 88,
      qrCode: '',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'stripe',
      payUrl: '/payment/stripe',
      outTradeNo: 'sub2_stripe_42',
      clientSecret: 'cs_test_42',
      payAmount: 88,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: 'resume-42',
      createdAt: Date.now(),
    }))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps polling and warns against duplicate payment when fulfillment becomes FAILED', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('FAILED'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(StripePaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
        },
      },
    })
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('payment.result.fulfillmentFailed')
    expect(wrapper.text()).toContain('payment.result.fulfillmentFailedHint')
    expect(wrapper.text()).not.toContain('payment.result.failed')

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('payment.result.success')
  })

  it('ignores and removes a legacy route client secret, using the order-bound session secret', async () => {
    routeState.query.client_secret = 'cs_attacker_order'

    const wrapper = mount(StripePaymentView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } },
    })
    await flushPromises()

    expect(loadStripe).toHaveBeenCalledWith('pk_test_42')
    expect(confirmWechatPayPayment).toHaveBeenCalledWith(
      'cs_test_42',
      expect.any(Object),
    )
    expect(routerReplace).toHaveBeenCalledWith({
      query: {
        order_id: '42',
        method: 'wechat_pay',
        resume_token: 'resume-42',
      },
    })
    expect(wrapper.text()).not.toContain('payment.stripeInvalidSession')
  })

  it('does not reconfirm an order that the backend already completed', async () => {
    getOrder.mockResolvedValue({ data: orderFactory('COMPLETED') })

    const wrapper = mount(StripePaymentView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } },
    })
    await flushPromises()

    expect(loadStripe).not.toHaveBeenCalled()
    expect(confirmWechatPayPayment).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.result.success')
  })

  it('single-flights slow polling requests', async () => {
    getOrder.mockResolvedValue({ data: orderFactory('PAID') })
    let resolvePoll!: (order: ReturnType<typeof orderFactory>) => void
    pollOrderStatus.mockImplementation(() => new Promise(resolve => { resolvePoll = resolve }))

    mount(StripePaymentView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } },
    })
    await flushPromises()

    await vi.advanceTimersByTimeAsync(6000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)

    resolvePoll(orderFactory('FAILED'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(3000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
  })
})
