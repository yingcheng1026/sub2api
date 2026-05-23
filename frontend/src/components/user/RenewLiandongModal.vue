<template>
  <!-- 续费 / 充值 SKU 选择 modal — 链动小铺直跳,绕开内置 ZPay -->
  <div
    v-if="show"
    class="fixed inset-0 z-[80] flex items-center justify-center bg-black/60 p-4"
    @click.self="emit('close')"
  >
    <div
      class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-2xl dark:bg-dark-800"
      @click.stop
    >
      <div class="mb-4 flex items-start justify-between">
        <div>
          <h2 class="text-lg font-bold text-gray-900 dark:text-white">
            {{ title || '选择续费 / 充值档位' }}
          </h2>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            点击下方任一档位将跳转链动小铺下单,完成后余额自动到账。
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

      <!-- 月卡 4 档 -->
      <div class="mb-5">
        <h3 class="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-300">
          月卡(固定 USD 配额,30 天有效)
        </h3>
        <div class="grid gap-2 sm:grid-cols-2">
          <button
            v-for="tier in LIANDONG_MONTHLY_TIERS"
            :key="tier.url"
            type="button"
            class="flex items-center justify-between rounded-lg border p-3 text-left transition-all hover:border-primary-500 hover:bg-primary-50 dark:border-dark-600 dark:hover:bg-dark-700"
            :class="recommendedTier?.url === tier.url
              ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
              : 'border-gray-200 bg-white dark:bg-dark-800'"
            @click="openLiandong(tier.url)"
          >
            <div>
              <div class="flex items-center gap-2">
                <span class="font-semibold text-gray-900 dark:text-white">{{ tier.name }}</span>
                <span
                  v-if="recommendedTier?.url === tier.url"
                  class="rounded bg-primary-500 px-1.5 py-0.5 text-[10px] font-medium text-white"
                >同档推荐</span>
              </div>
              <div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                ${{ tier.quotaUsd.toLocaleString() }} USD 月配额
              </div>
            </div>
            <div class="text-right">
              <div class="text-base font-bold text-primary-600 dark:text-primary-400">
                ¥{{ tier.priceCny }}
              </div>
            </div>
          </button>
        </div>
      </div>

      <!-- 通用余额 3 档 -->
      <div class="mb-5">
        <h3 class="mb-2 text-sm font-semibold text-gray-700 dark:text-gray-300">
          通用余额(永久有效,按渠道倍率消耗)
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

      <!-- 自定义额度兜底 -->
      <div class="rounded-lg bg-amber-50 p-3 text-xs dark:bg-amber-900/20">
        <p class="text-amber-800 dark:text-amber-300">
          想要自定义额度(例如 $50、$1000)?这些档位以外
          <button
            type="button"
            class="underline hover:text-amber-900 dark:hover:text-amber-200"
            @click="copyCustomWechat"
          >
            联系管理员微信 <code class="font-mono font-semibold">{{ LIANDONG_CUSTOM_WECHAT }}</code>
          </button>
          手动开单。
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import type { UserSubscription } from '@/types'
import {
  LIANDONG_MONTHLY_TIERS,
  LIANDONG_CREDITS_TIERS,
  LIANDONG_CUSTOM_WECHAT,
  matchMonthlyTier
} from '@/constants/liandongSku'

const props = defineProps<{
  show: boolean
  /** 可选,用于按 group.monthly_limit_usd 高亮"同档推荐"月卡;Dashboard 充值场景可不传 */
  subscription?: UserSubscription | null
  /** 可选标题覆盖,默认"选择续费 / 充值档位" */
  title?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()

const recommendedTier = computed(() => {
  const sub = props.subscription
  if (!sub) return null
  return matchMonthlyTier(sub.group?.monthly_limit_usd)
})

function openLiandong(url: string) {
  window.open(url, '_blank', 'noopener')
  emit('close')
}

function copyCustomWechat() {
  navigator.clipboard?.writeText(LIANDONG_CUSTOM_WECHAT).catch(() => {})
  appStore.showSuccess?.(`已复制微信号 ${LIANDONG_CUSTOM_WECHAT}`)
}
</script>
