<template>
  <div
    class="overflow-hidden rounded-2xl border border-amber-200 bg-gradient-to-br from-amber-50 via-white to-orange-50 dark:border-amber-900/40 dark:from-amber-950/40 dark:via-dark-800 dark:to-orange-950/40"
  >
    <!-- Header -->
    <div class="flex items-center justify-between border-b border-amber-100 p-4 dark:border-amber-900/40">
      <div class="flex items-center gap-3">
        <div class="flex h-10 w-10 items-center justify-center rounded-xl bg-amber-500/10 text-amber-600 dark:text-amber-400">
          <Icon name="creditCard" size="lg" />
        </div>
        <div>
          <h3 class="font-semibold text-gray-900 dark:text-white">
            {{ t('userSubscriptions.wallet.title') }}
          </h3>
          <p class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('userSubscriptions.wallet.subtitle') }}
          </p>
        </div>
      </div>
      <span
        :class="[
          'rounded-full px-2 py-0.5 text-xs font-medium',
          subscription.status === 'active'
            ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
            : subscription.status === 'expired'
              ? 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
              : 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
        ]"
      >
        {{ t(`userSubscriptions.status.${subscription.status}`) }}
      </span>
    </div>

    <div class="space-y-4 p-4">
      <!-- Balance number + percentage -->
      <div class="flex items-end justify-between">
        <div>
          <div class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
            {{ t('userSubscriptions.wallet.remaining') }}
          </div>
          <div class="mt-1 flex items-baseline gap-2">
            <span class="text-3xl font-bold text-amber-700 dark:text-amber-300">
              ${{ remaining.toFixed(2) }}
            </span>
            <span v-if="initial > 0" class="text-sm text-gray-500 dark:text-dark-400">
              / ${{ initial.toFixed(2) }}
            </span>
          </div>
        </div>
        <div class="text-right">
          <div class="text-xs uppercase tracking-wide text-gray-500 dark:text-dark-400">
            {{ t('userSubscriptions.wallet.usedPercent') }}
          </div>
          <div class="mt-1 text-2xl font-semibold text-gray-700 dark:text-gray-200">
            {{ usedPercent.toFixed(0) }}%
          </div>
        </div>
      </div>

      <!-- Progress bar -->
      <div class="relative h-2.5 overflow-hidden rounded-full bg-amber-100/60 dark:bg-amber-900/30">
        <div
          class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
          :class="progressBarClass"
          :style="{ width: `${Math.min(100, usedPercent)}%` }"
        ></div>
      </div>

      <!-- Renew CTA when low or out -->
      <div v-if="isLow" class="rounded-xl border border-amber-300 bg-amber-50/80 p-3 text-sm dark:border-amber-800 dark:bg-amber-950/40">
        <div class="flex items-start gap-2">
          <Icon name="infoCircle" class="mt-0.5 shrink-0 text-amber-600 dark:text-amber-400" />
          <div class="flex-1">
            <p class="font-medium text-amber-900 dark:text-amber-200">
              {{ remaining <= 0
                ? t('userSubscriptions.wallet.exhausted')
                : t('userSubscriptions.wallet.lowWarning', { amount: remaining.toFixed(2) })
              }}
            </p>
            <button
              class="mt-2 rounded-lg bg-amber-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-amber-700"
              @click="$emit('renew')"
            >
              {{ t('payment.renewNow') }}
            </button>
          </div>
        </div>
      </div>

      <!-- Expiration -->
      <div class="flex items-center justify-between text-sm">
        <span class="text-gray-500 dark:text-dark-400">{{ t('userSubscriptions.expires') }}</span>
        <span class="text-gray-700 dark:text-gray-300">
          {{ subscription.expires_at ? formatDateOnly(subscription.expires_at) : t('userSubscriptions.noExpiration') }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UserSubscription } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import { formatDateOnly } from '@/utils/format'

const props = defineProps<{ subscription: UserSubscription }>()
defineEmits<{ (e: 'renew'): void }>()

const { t } = useI18n()

const remaining = computed(() => Number(props.subscription.wallet_balance_usd ?? 0))
const initial = computed(() => Number(props.subscription.wallet_initial_usd ?? 0))
const usedPercent = computed(() => {
  if (initial.value <= 0) return 0
  const used = initial.value - remaining.value
  return Math.max(0, (used / initial.value) * 100)
})
const isLow = computed(() => remaining.value <= 150)
const progressBarClass = computed(() => {
  if (usedPercent.value >= 95) return 'bg-red-500'
  if (usedPercent.value >= 80) return 'bg-amber-500'
  return 'bg-emerald-500'
})
</script>
