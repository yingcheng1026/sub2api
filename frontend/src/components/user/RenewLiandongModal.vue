<template>
  <!-- 额度充值 SKU 选择 modal — 链动小铺直跳 -->
  <div
    v-if="show"
    data-hfc-liandong-renew-modal="wallet"
    class="fixed inset-0 z-[80] flex items-center justify-center bg-black/60 p-4"
    @click.self="emit('close')"
  >
    <div
      class="max-h-[calc(100vh-2rem)] w-full max-w-2xl overflow-y-auto rounded-2xl bg-white p-6 shadow-2xl dark:bg-dark-800"
      @click.stop
    >
      <div class="mb-4 flex items-start justify-between">
        <div>
          <h2 class="text-lg font-bold text-gray-900 dark:text-white">
            {{ title || '选择充值额度' }}
          </h2>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            点击档位后跳转链动小铺下单；付款后获取兑换码，再回个人中心完成兑换。
          </p>
          <p class="mt-1 text-xs text-amber-600 dark:text-amber-300">
            兑换码从订单发放时开始 30 天有效,未发放库存码不计时。
          </p>
        </div>
        <button
          class="rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700"
          @click="emit('close')"
          aria-label="close"
        >
          <Icon name="x" size="md" />
        </button>
      </div>

      <div class="mb-5">
        <h3 class="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-300">
          通用余额（按实际调用扣费）
        </h3>
        <div class="grid gap-2 sm:grid-cols-3">
          <button
            v-for="tier in LIANDONG_CREDITS_TIERS"
            :key="tier.url"
            type="button"
            class="flex flex-col items-center justify-center rounded-lg border border-gray-200 bg-white p-3 transition-all hover:border-primary-500 hover:bg-primary-50 dark:border-dark-600 dark:bg-dark-800 dark:hover:bg-dark-700"
            @click="openLiandong(tier.url)"
          >
            <span class="text-base font-bold text-gray-900 dark:text-white">
              ${{ tier.creditsUsd }}
            </span>
            <span class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              ¥{{ tier.priceCny }} = ${{ tier.creditsUsd }} 余额
            </span>
          </button>
        </div>
      </div>

      <a
        href="/redeem"
        class="mb-5 block rounded-lg border border-primary-200 bg-primary-50 p-3 text-center text-sm font-semibold text-primary-700 hover:bg-primary-100 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-300"
      >
        已拿到兑换码？去个人中心兑换
      </a>

      <!-- 自定义额度兜底 -->
      <div class="rounded-lg bg-amber-50 p-3 text-xs dark:bg-amber-900/20">
        <p class="text-amber-800 dark:text-amber-300">
          想要自定义额度(例如 $50、$1000)?这些档位以外
          请联系管理员或客服人工自定义充值；按客服确认的方式完成转账，管理员核对后在后台入账。
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { LIANDONG_CREDITS_TIERS } from '@/constants/liandongSku'
defineProps<{
  show: boolean
  /** 可选标题覆盖，默认“选择充值额度” */
  title?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

function openLiandong(url: string) {
  window.open(url, '_blank', 'noopener')
  emit('close')
}

</script>
