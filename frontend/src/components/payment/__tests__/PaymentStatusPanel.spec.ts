import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const pollOrderStatus = vi.hoisted(() => vi.fn())
const cancelOrder = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const toCanvas = vi.hoisted(() => vi.fn())

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

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
  }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    cancelOrder,
  },
}))

vi.mock('qrcode', () => ({
  default: {
    toCanvas,
  },
}))

import PaymentStatusPanel from '../PaymentStatusPanel.vue'

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
  expires_at: '2099-01-01T12:30:00Z',
  refund_amount: 0,
  paid_at: ['PAID', 'RECHARGING', 'COMPLETED', 'FAILED'].includes(status)
    ? '2026-04-20T12:00:01Z'
    : undefined,
})

describe('PaymentStatusPanel', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    pollOrderStatus.mockReset()
    cancelOrder.mockReset()
    showError.mockReset()
    toCanvas.mockReset().mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps polling through RECHARGING and succeeds only after COMPLETED', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('RECHARGING'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: 'https://pay.example.com/qr/42',
        expiresAt: '2099-01-01T12:30:00Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    await flushPromises()
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledWith(42)
    expect(wrapper.text()).not.toContain('payment.result.success')
    expect(wrapper.emitted('success')).toBeUndefined()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.success')
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('keeps a paid fulfillment failure recoverable instead of expiring or settling it', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('FAILED'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: 'https://pay.example.com/qr/42',
        expiresAt: '2099-01-01T12:30:00Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.fulfillmentFailed')
    expect(wrapper.text()).toContain('payment.result.fulfillmentFailedHint')
    expect(wrapper.text()).not.toContain('payment.qr.expired')
    expect(wrapper.emitted('settled')).toBeUndefined()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('keeps an unpaid provider FAILED order terminal', async () => {
    pollOrderStatus.mockResolvedValueOnce({
      ...orderFactory('FAILED'),
      paid_at: undefined,
    })

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: '',
        expiresAt: '2099-01-01T12:30:00Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.qr.expired')
    expect(wrapper.text()).not.toContain('payment.result.fulfillmentFailed')
    expect(wrapper.emitted('settled')).toEqual([['expired']])
  })

  it('shows reopen button in QR mode when payUrl is also available', async () => {
    const openSpy = vi.spyOn(window, 'open').mockReturnValue({ closed: false } as Window)

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: 'https://pay.example.com/qr/42',
        payUrl: 'https://pay.example.com/session/42',
        expiresAt: '2099-01-01T12:30:00Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    await flushPromises()
    expect(wrapper.text()).toContain('payment.qr.openPayWindow')

    await wrapper.get('button.btn.btn-secondary.text-sm').trigger('click')
    expect(openSpy).toHaveBeenCalledWith(
      'https://pay.example.com/session/42',
      'paymentPopup',
      expect.any(String),
    )

    openSpy.mockRestore()
  })

  it('does a final status check at the deadline and keeps polling a PAID order', async () => {
    vi.setSystemTime(new Date('2026-04-20T12:00:00Z'))
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PAID'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: '',
        expiresAt: '2026-04-20T12:00:01Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')
    expect(wrapper.emitted('success')).toBeUndefined()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('does not invent a terminal failure when the deadline check is still PENDING', async () => {
    vi.setSystemTime(new Date('2026-04-20T12:00:00Z'))
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PENDING'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: '',
        expiresAt: '2026-04-20T12:00:01Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('keeps polling EXPIRED during the backend late-payment recovery grace', async () => {
    vi.setSystemTime(new Date('2026-04-20T12:00:00Z'))
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('EXPIRED'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: '',
        expiresAt: '2026-04-20T12:00:01Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(wrapper.text()).not.toContain('payment.qr.expired')
    expect(wrapper.emitted('settled')).toBeUndefined()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('never downgrades an already PAID order to expired at the deadline', async () => {
    vi.setSystemTime(new Date('2026-04-20T12:00:00Z'))
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PAID'))
      .mockResolvedValueOnce(orderFactory('PENDING'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: '',
        expiresAt: '2026-04-20T12:00:04Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('payment.qr.expired')

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('does not report cancellation when the backend says the provider was already paid', async () => {
    cancelOrder.mockResolvedValue({ data: { message: 'already_paid' } })
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PAID'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentStatusPanel, {
      props: {
        orderId: 42,
        qrCode: 'https://pay.example.com/qr/42',
        expiresAt: '2099-01-01T12:30:00Z',
        paymentType: 'alipay',
        orderType: 'balance',
      },
      global: { stubs: { Icon: true } },
    })

    await wrapper.get('button.btn.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('settled')).toBeUndefined()
    expect(wrapper.text()).toContain('payment.result.processing')

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(wrapper.emitted('success')).toHaveLength(1)
  })
})
