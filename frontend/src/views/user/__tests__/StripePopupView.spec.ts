import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const routeState = vi.hoisted(() => ({
  query: {
    order_id: '42',
    method: 'wechat_pay',
    amount: '88',
  } as Record<string, unknown>,
}))
const confirmWechatPayPayment = vi.hoisted(() => vi.fn())
const loadStripe = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => routeState }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/utils/device', () => ({ isMobileDevice: () => false }))
vi.mock('@stripe/stripe-js', () => ({ loadStripe }))

import StripePopupView from '../StripePopupView.vue'
import { clearSessionAccessToken, setSessionAccessToken } from '@/auth/browserSession'

describe('StripePopupView fulfillment polling', () => {
  const fetchMock = vi.fn()

  beforeEach(() => {
    vi.useFakeTimers()
    confirmWechatPayPayment.mockReset().mockResolvedValue({ paymentIntent: { status: 'succeeded' } })
    loadStripe.mockReset().mockResolvedValue({ confirmWechatPayPayment })
    fetchMock.mockReset().mockResolvedValue({
      ok: true,
      json: async () => ({ data: { status: 'CANCELLED' } }),
    })
    vi.stubGlobal('fetch', fetchMock)
    window.localStorage.clear()
    setSessionAccessToken('auth-token-42')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    clearSessionAccessToken()
    vi.useRealTimers()
  })

  it('stops polling and shows failure when fulfillment becomes CANCELLED', async () => {
    const wrapper = mount(StripePopupView)
    window.dispatchEvent(new MessageEvent('message', {
      origin: window.location.origin,
      data: {
        type: 'STRIPE_POPUP_INIT',
        clientSecret: 'cs_test_42',
        publishableKey: 'pk_test_42',
      },
    }))
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/payment/orders/42',
      expect.objectContaining({
        headers: { Authorization: 'Bearer auth-token-42' },
      }),
    )
    expect(wrapper.text()).toContain('payment.result.failed')

    await vi.advanceTimersByTimeAsync(6000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('keeps polling and warns when paid fulfillment becomes FAILED', async () => {
    fetchMock
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ data: { status: 'FAILED', paid_at: '2026-04-20T12:00:01Z' } }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ data: { status: 'COMPLETED' } }),
      })

    const wrapper = mount(StripePopupView)
    window.dispatchEvent(new MessageEvent('message', {
      origin: window.location.origin,
      data: {
        type: 'STRIPE_POPUP_INIT',
        clientSecret: 'cs_test_42',
        publishableKey: 'pk_test_42',
      },
    }))
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.fulfillmentFailedHint')
    expect(wrapper.text()).not.toContain('payment.result.failed')

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('payment.result.success')
  })

  it('uses the 15 second timer only for popup initialization, not for user payment time', async () => {
    confirmWechatPayPayment.mockImplementation(() => new Promise(() => {}))

    const wrapper = mount(StripePopupView)
    window.dispatchEvent(new MessageEvent('message', {
      origin: window.location.origin,
      data: {
        type: 'STRIPE_POPUP_INIT',
        clientSecret: 'cs_test_42',
        publishableKey: 'pk_test_42',
      },
    }))
    await flushPromises()

    await vi.advanceTimersByTimeAsync(15000)
    expect(wrapper.text()).not.toContain('payment.stripePopup.timeout')
  })

  it('single-flights slow status requests', async () => {
    let resolveFetch!: (value: { ok: boolean; json: () => Promise<unknown> }) => void
    fetchMock.mockImplementation(() => new Promise(resolve => { resolveFetch = resolve }))

    mount(StripePopupView)
    window.dispatchEvent(new MessageEvent('message', {
      origin: window.location.origin,
      data: {
        type: 'STRIPE_POPUP_INIT',
        clientSecret: 'cs_test_42',
        publishableKey: 'pk_test_42',
      },
    }))
    await flushPromises()

    await vi.advanceTimersByTimeAsync(6000)
    expect(fetchMock).toHaveBeenCalledTimes(1)

    resolveFetch({
      ok: true,
      json: async () => ({ data: { status: 'FAILED', paid_at: '2026-04-20T12:00:01Z' } }),
    })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})
