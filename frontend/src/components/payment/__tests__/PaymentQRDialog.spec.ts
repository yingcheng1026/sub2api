import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const pollOrderStatus = vi.hoisted(() => vi.fn())
const cancelOrder = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/stores/payment', () => ({ usePaymentStore: () => ({ pollOrderStatus }) }))
vi.mock('@/api/payment', () => ({ paymentAPI: { cancelOrder } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('qrcode', () => ({ default: { toCanvas: vi.fn() } }))

import PaymentQRDialog from '../PaymentQRDialog.vue'

const orderFactory = (status: string) => ({
  id: 42,
  user_id: 9,
  amount: 88,
  pay_amount: 88,
  fee_rate: 0,
  payment_type: 'alipay',
  out_trade_no: 'sub2_dialog_42',
  status,
  order_type: 'balance',
  created_at: '2026-04-20T12:00:00Z',
  expires_at: '2026-04-20T12:00:01Z',
  refund_amount: 0,
  paid_at: ['PAID', 'RECHARGING', 'COMPLETED', 'FAILED'].includes(status)
    ? '2026-04-20T12:00:01Z'
    : undefined,
})

describe('PaymentQRDialog deadline fulfillment', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-04-20T12:00:00Z'))
    pollOrderStatus.mockReset()
    cancelOrder.mockReset()
    showError.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('does not expire a PAID order and waits for COMPLETED', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('PAID'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentQRDialog, {
      props: {
        show: false,
        orderId: 42,
        qrCode: '',
        expiresAt: '2026-04-20T12:00:01Z',
        paymentType: 'alipay',
      },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
        },
      },
    })

    await wrapper.setProps({ show: true })
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

  it('does not label a retryable FAILED fulfillment as an expired order', async () => {
    pollOrderStatus
      .mockResolvedValueOnce(orderFactory('FAILED'))
      .mockResolvedValueOnce(orderFactory('COMPLETED'))

    const wrapper = mount(PaymentQRDialog, {
      props: {
        show: false,
        orderId: 42,
        qrCode: '',
        expiresAt: '2026-04-20T12:00:01Z',
        paymentType: 'alipay',
      },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
        },
      },
    })

    await wrapper.setProps({ show: true })
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.fulfillmentFailed')
    expect(wrapper.text()).not.toContain('payment.qr.expired')

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('success')).toHaveLength(1)
  })
})
