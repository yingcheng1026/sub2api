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
  it('marks the 29.9 trial monthly package as limited to one purchase', () => {
    const wrapper = mountRenewModal()
    const text = wrapper.get('[data-hfc-liandong-renew-modal="wallet"]').text()

    expect(text).toContain('体验版')
    expect(text).toContain('¥29.9')
    expect(text).toContain('限购一次')
  })
})
