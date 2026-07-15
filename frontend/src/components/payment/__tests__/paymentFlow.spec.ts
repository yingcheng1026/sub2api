import { describe, expect, it } from 'vitest'
import type { CreateOrderResult, MethodLimit } from '@/types/payment'
import {
  PAYMENT_LATE_SETTLEMENT_GRACE_MS,
  PAYMENT_RECOVERY_RETENTION_MS,
  PAYMENT_RECOVERY_STORAGE_KEY,
  buildCreateOrderPayload,
  decidePaymentLaunch,
  getVisibleMethods,
  isPaymentCompleted,
  isPaymentFulfillmentFailed,
  isPaymentFulfillmentPending,
  isPaymentLateSettlementRecoverable,
  isPaymentStillProcessing,
  isPaymentTerminalFailure,
  clearPaymentRecoveryForUser,
  clearPaymentRecoverySnapshot,
  readPaymentRecoverySnapshot,
  writePaymentRecoverySnapshot,
  type PaymentRecoverySnapshot,
} from '@/components/payment/paymentFlow'

describe('payment fulfillment status', () => {
  it('treats only COMPLETED as delivered', () => {
    expect(isPaymentCompleted('COMPLETED')).toBe(true)
    expect(isPaymentCompleted(' completed ')).toBe(true)
    expect(isPaymentCompleted('PAID')).toBe(false)
    expect(isPaymentCompleted('RECHARGING')).toBe(false)
  })

  it('keeps paid and recharging orders in the processing state', () => {
    expect(isPaymentStillProcessing('PAID')).toBe(true)
    expect(isPaymentStillProcessing('RECHARGING')).toBe(true)
    expect(isPaymentStillProcessing('PENDING')).toBe(true)
    expect(isPaymentStillProcessing('COMPLETED')).toBe(false)
  })

  it('distinguishes captured payment fulfillment from pre-payment waiting', () => {
    expect(isPaymentFulfillmentPending('PAID')).toBe(true)
    expect(isPaymentFulfillmentPending('RECHARGING')).toBe(true)
    expect(isPaymentFulfillmentPending('FAILED', '2026-04-20T12:00:01Z')).toBe(true)
    expect(isPaymentFulfillmentPending('FAILED')).toBe(false)
    expect(isPaymentFulfillmentPending('PENDING')).toBe(false)
  })

  it('keeps retryable fulfillment failures separate from terminal payment failures', () => {
    expect(isPaymentFulfillmentFailed('FAILED', '2026-04-20T12:00:01Z')).toBe(true)
    expect(isPaymentFulfillmentFailed('FAILED')).toBe(false)
    expect(isPaymentFulfillmentFailed('EXPIRED')).toBe(false)
    expect(isPaymentTerminalFailure('FAILED')).toBe(true)
    expect(isPaymentTerminalFailure('FAILED', '2026-04-20T12:00:01Z')).toBe(false)
    expect(isPaymentTerminalFailure('CANCELLED')).toBe(true)
    expect(isPaymentTerminalFailure('EXPIRED')).toBe(true)
    expect(isPaymentTerminalFailure('PAID')).toBe(false)
  })

  it('keeps an expired provider order recoverable only during the late-webhook grace', () => {
    const expiresAt = '2026-04-20T12:00:00.000Z'
    const expiryMs = Date.parse(expiresAt)

    expect(isPaymentLateSettlementRecoverable(
      'EXPIRED',
      null,
      expiresAt,
      expiryMs + PAYMENT_LATE_SETTLEMENT_GRACE_MS - 1,
    )).toBe(true)
    expect(isPaymentLateSettlementRecoverable(
      'EXPIRED',
      null,
      expiresAt,
      expiryMs + PAYMENT_LATE_SETTLEMENT_GRACE_MS + 1,
    )).toBe(false)
    expect(isPaymentLateSettlementRecoverable('FAILED', null, expiresAt, expiryMs + 1)).toBe(false)
  })
})

function methodLimit(overrides: Partial<MethodLimit> = {}): MethodLimit {
  return {
    daily_limit: 0,
    daily_used: 0,
    daily_remaining: 0,
    single_min: 0,
    single_max: 0,
    fee_rate: 0,
    available: true,
    ...overrides,
  }
}

function createOrderResult(overrides: Partial<CreateOrderResult> = {}): CreateOrderResult {
  return {
    order_id: 101,
    amount: 88,
    pay_amount: 88,
    fee_rate: 0,
    expires_at: '2099-01-01T00:10:00.000Z',
    ...overrides,
  }
}

describe('getVisibleMethods', () => {
  it('normalizes provider aliases and keeps stripe as a top-level method', () => {
    const visible = getVisibleMethods({
      alipay_direct: methodLimit({ single_min: 5 }),
      wxpay: methodLimit({ single_max: 100 }),
      stripe: methodLimit({ fee_rate: 3 }),
    })

    expect(visible).toEqual({
      alipay: methodLimit({ single_min: 5 }),
      wxpay: methodLimit({ single_max: 100 }),
      stripe: methodLimit({ fee_rate: 3 }),
    })
  })

  it('prefers canonical visible methods over aliases when both exist', () => {
    const visible = getVisibleMethods({
      alipay: methodLimit({ single_min: 2 }),
      alipay_direct: methodLimit({ single_min: 9 }),
      wxpay_direct: methodLimit({ fee_rate: 1.2 }),
    })

    expect(visible.alipay.single_min).toBe(2)
    expect(visible.wxpay.fee_rate).toBe(1.2)
  })
})

describe('decidePaymentLaunch', () => {
  it('uses Stripe popup waiting flow for desktop Alipay client secret', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      client_secret: 'cs_test',
      resume_token: 'resume-1',
    }), {
      visibleMethod: 'alipay',
      orderType: 'balance',
      isMobile: false,
    })

    expect(decision.kind).toBe('stripe_popup')
    expect(decision.paymentState.paymentType).toBe('alipay')
    expect(decision.stripeMethod).toBe('alipay')
    expect(decision.recovery.resumeToken).toBe('resume-1')
    expect(decision.recovery.outTradeNo).toBe('')
  })

  it('routes Stripe button click to the full Payment Element without a preselected sub-method', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      client_secret: 'cs_test',
    }), {
      visibleMethod: 'stripe',
      orderType: 'balance',
      isMobile: false,
    })

    expect(decision.kind).toBe('stripe_route')
    expect(decision.stripeMethod).toBeUndefined()
  })

  it('uses Stripe route flow for mobile WeChat client secret', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      client_secret: 'cs_test',
    }), {
      visibleMethod: 'wxpay',
      orderType: 'subscription',
      isMobile: true,
    })

    expect(decision.kind).toBe('stripe_route')
    expect(decision.stripeMethod).toBe('wechat_pay')
    expect(decision.paymentState.orderType).toBe('subscription')
  })

  it('keeps hosted redirect metadata for recovery flows', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      pay_url: 'https://pay.example.com/session/abc',
      payment_mode: 'popup',
      resume_token: 'resume-2',
      out_trade_no: 'sub2_abc',
    }), {
      visibleMethod: 'wxpay',
      orderType: 'balance',
      isMobile: false,
    })

    expect(decision.kind).toBe('redirect_waiting')
    expect(decision.paymentState.payUrl).toBe('https://pay.example.com/session/abc')
    expect(decision.recovery.paymentMode).toBe('popup')
    expect(decision.recovery.outTradeNo).toBe('sub2_abc')
    expect(decision.recovery.resumeToken).toBe('resume-2')
  })

  it('prefers redirect on mobile when both pay_url and qr_code are present', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      pay_url: 'https://pay.example.com/mobile/session',
      qr_code: 'https://pay.example.com/qr/session',
    }), {
      visibleMethod: 'alipay',
      orderType: 'balance',
      isMobile: true,
    })

    expect(decision.kind).toBe('redirect_waiting')
    expect(decision.paymentState.payUrl).toBe('https://pay.example.com/mobile/session')
    expect(decision.paymentState.qrCode).toBe('https://pay.example.com/qr/session')
  })

  it('keeps QR flow on desktop when both pay_url and qr_code are present', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      pay_url: 'https://pay.example.com/desktop/session',
      qr_code: 'https://pay.example.com/qr/session',
    }), {
      visibleMethod: 'wxpay',
      orderType: 'balance',
      isMobile: false,
    })

    expect(decision.kind).toBe('qr_waiting')
    expect(decision.paymentState.qrCode).toBe('https://pay.example.com/qr/session')
  })

  it('returns wechat oauth launch when backend requires in-app authorization', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      result_type: 'oauth_required',
      payment_type: 'wxpay',
      oauth: {
        authorize_url: '/api/v1/auth/oauth/wechat/payment/start?payment_type=wxpay',
        appid: 'wx123',
        scope: 'snsapi_base',
        redirect_url: '/auth/wechat/payment/callback',
      },
    }), {
      visibleMethod: 'wxpay',
      orderType: 'balance',
      isMobile: true,
    })

    expect(decision.kind).toBe('wechat_oauth')
    expect(decision.oauth?.authorize_url).toContain('/api/v1/auth/oauth/wechat/payment/start')
    expect(decision.paymentState.paymentType).toBe('wxpay')
  })

  it('returns wechat jsapi launch when backend has a jsapi payload ready', () => {
    const decision = decidePaymentLaunch(createOrderResult({
      result_type: 'jsapi_ready',
      payment_type: 'wxpay',
      jsapi: {
        appId: 'wx123',
        timeStamp: '1712345678',
        nonceStr: 'nonce-123',
        package: 'prepay_id=wx123',
        signType: 'RSA',
        paySign: 'signed-payload',
      },
    }), {
      visibleMethod: 'wxpay',
      orderType: 'subscription',
      isMobile: true,
    })

    expect(decision.kind).toBe('wechat_jsapi')
    expect(decision.jsapi?.appId).toBe('wx123')
    expect(decision.paymentState.orderType).toBe('subscription')
  })
})

describe('buildCreateOrderPayload', () => {
  it('normalizes visible method aliases and attaches a canonical result URL', () => {
    expect(buildCreateOrderPayload({
      amount: 88,
      paymentType: 'alipay_direct',
      orderType: 'balance',
      origin: 'https://app.example.com/',
      isMobile: true,
      isWechatBrowser: false,
    })).toEqual({
      amount: 88,
      payment_type: 'alipay',
      order_type: 'balance',
      return_url: 'https://app.example.com/payment/result',
      is_mobile: true,
      payment_source: 'hosted_redirect',
    })
  })

  it('uses WeChat in-app resume source for visible WeChat payments in the WeChat browser', () => {
    expect(buildCreateOrderPayload({
      amount: 128,
      paymentType: 'wxpay',
      orderType: 'subscription',
      planId: 7,
      origin: 'https://app.example.com',
      isMobile: false,
      isWechatBrowser: true,
    })).toEqual({
      amount: 128,
      payment_type: 'wxpay',
      order_type: 'subscription',
      plan_id: 7,
      return_url: 'https://app.example.com/payment/result',
      is_mobile: false,
      payment_source: 'wechat_in_app_resume',
    })
  })
})

describe('readPaymentRecoverySnapshot', () => {
  it('restores an unexpired snapshot when the resume token matches', () => {
    const snapshot: PaymentRecoverySnapshot = {
      orderId: 33,
      amount: 18,
      qrCode: '',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: 'https://pay.example.com/session/33',
      outTradeNo: 'sub2_33',
      clientSecret: '',
      payAmount: 18,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: 'resume-33',
      createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
    }

    const restored = readPaymentRecoverySnapshot(JSON.stringify(snapshot), {
      now: Date.UTC(2099, 0, 1, 0, 1, 0),
      resumeToken: 'resume-33',
    })

    expect(restored?.orderId).toBe(33)
  })

  it('keeps expired recovery identity for backend verification but rejects a mismatched token', () => {
    const expiredSnapshot: PaymentRecoverySnapshot = {
      orderId: 55,
      amount: 18,
      qrCode: '',
      expiresAt: '2024-01-01T00:10:00.000Z',
      paymentType: 'wxpay',
      payUrl: 'https://pay.example.com/session/55',
      outTradeNo: 'sub2_55',
      clientSecret: '',
      payAmount: 18,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: 'resume-55',
      createdAt: Date.UTC(2024, 0, 1, 0, 0, 0),
    }

    expect(readPaymentRecoverySnapshot(JSON.stringify(expiredSnapshot), {
      now: Date.UTC(2024, 0, 1, 0, 20, 0),
      resumeToken: 'resume-55',
    })?.orderId).toBe(55)

    expect(readPaymentRecoverySnapshot(JSON.stringify({
      ...expiredSnapshot,
      outTradeNo: 'sub2_55',
      expiresAt: '2099-01-01T00:10:00.000Z',
    }), {
      now: Date.UTC(2099, 0, 1, 0, 1, 0),
      resumeToken: 'other-token',
    })).toBeNull()
  })

  it('keeps backward compatibility with snapshots written before outTradeNo existed', () => {
    const restored = readPaymentRecoverySnapshot(JSON.stringify({
      orderId: 44,
      amount: 18,
      qrCode: '',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: 'https://pay.example.com/session/44',
      clientSecret: '',
      payAmount: 18,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: 'resume-44',
      createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
    }), {
      now: Date.UTC(2099, 0, 1, 0, 1, 0),
      resumeToken: 'resume-44',
    })

    expect(restored?.orderId).toBe(44)
    expect(restored?.outTradeNo).toBe('')
  })

  it('stores multiple user-bound slots and compare-clears only the expected revision', () => {
    const values = new Map<string, string>()
    const storage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
      removeItem: (key: string) => { values.delete(key) },
    }
    const base: PaymentRecoverySnapshot = {
      orderId: 101,
      userId: 7,
      revision: 'rev-101',
      amount: 18,
      qrCode: '',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: '',
      outTradeNo: 'sub2_101',
      clientSecret: '',
      payAmount: 18,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: '',
      createdAt: Date.now(),
    }
    writePaymentRecoverySnapshot(storage, base)
    writePaymentRecoverySnapshot(storage, {
      ...base,
      orderId: 202,
      userId: 8,
      revision: 'rev-202',
      outTradeNo: 'sub2_202',
    })

    const raw = storage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)
    expect(readPaymentRecoverySnapshot(raw, { userId: 7, orderId: 101 })?.revision).toBe('rev-101')
    expect(readPaymentRecoverySnapshot(raw, { userId: 7, orderId: 202 })).toBeNull()
    expect(clearPaymentRecoverySnapshot(storage, {
      userId: 7,
      orderId: 101,
      revision: 'wrong-revision',
    })).toBe(false)
    expect(clearPaymentRecoverySnapshot(storage, {
      userId: 7,
      orderId: 101,
      revision: 'rev-101',
    })).toBe(true)
    expect(readPaymentRecoverySnapshot(storage.getItem(PAYMENT_RECOVERY_STORAGE_KEY), {
      userId: 8,
      orderId: 202,
    })?.orderId).toBe(202)
  })

  it('clears only the requested user and rejects retained launch data after the retention window', () => {
    const values = new Map<string, string>()
    const storage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
      removeItem: (key: string) => { values.delete(key) },
    }
    const createdAt = Date.now()
    const makeSnapshot = (userId: number, orderId: number): PaymentRecoverySnapshot => ({
      orderId,
      userId,
      revision: `rev-${orderId}`,
      amount: 18,
      qrCode: 'secret-qr',
      expiresAt: '2026-04-20T12:10:00.000Z',
      paymentType: 'stripe',
      payUrl: '/payment/stripe?secret',
      outTradeNo: `sub2_${orderId}`,
      clientSecret: 'cs_secret',
      payAmount: 18,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: 'resume-secret',
      createdAt,
    })
    writePaymentRecoverySnapshot(storage, makeSnapshot(7, 101))
    writePaymentRecoverySnapshot(storage, makeSnapshot(8, 202))

    expect(clearPaymentRecoveryForUser(storage, 7)).toBe(1)
    expect(readPaymentRecoverySnapshot(storage.getItem(PAYMENT_RECOVERY_STORAGE_KEY), {
      userId: 8,
      orderId: 202,
      now: createdAt + PAYMENT_RECOVERY_RETENTION_MS + 1,
    })).toBeNull()
  })
})
