import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import RenewLiandongModal from '../RenewLiandongModal.vue'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: vi.fn()
  })
}))

function mountRenewModal() {
  return mount(RenewLiandongModal, {
    props: {
      show: true
    },
    global: {
      stubs: {
        Icon: {
          props: ['name', 'size'],
          template: '<span />'
        }
      }
    }
  })
}

describe('RenewLiandongModal', () => {
  it('only presents credit top-up tiers and the redeem flow', () => {
    const wrapper = mountRenewModal()
    const text = wrapper.get('[data-hfc-liandong-renew-modal="wallet"]').text()

    expect(text).toContain('$30')
    expect(text).toContain('$100')
    expect(text).toContain('$500')
    expect(text).toContain('兑换码')
    expect(text).not.toContain('月卡')
    expect(text).not.toContain('自动到账')
  })
})
