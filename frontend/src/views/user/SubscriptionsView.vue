<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- Loading State -->
      <div v-if="loading" class="flex justify-center py-12">
        <div
          class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"
        ></div>
      </div>

      <!-- Empty State -->
      <div v-else-if="subscriptions.length === 0" class="card p-12 text-center">
        <div
          class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700"
        >
          <Icon name="creditCard" size="xl" class="text-gray-400" />
        </div>
        <h3 class="mb-2 text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('userSubscriptions.noActiveSubscriptions') }}
        </h3>
        <p class="text-gray-500 dark:text-dark-400">
          {{ t('userSubscriptions.noActiveSubscriptionsDesc') }}
        </p>
      </div>

      <!-- 钱包模式 (v4)：钱包卡 + 全 group 倍率列表 -->
      <div v-else-if="walletSubscriptions.length > 0" class="grid gap-6 lg:grid-cols-2">
        <WalletBalanceCard
          v-for="sub in walletSubscriptions"
          :key="sub.id"
          :subscription="sub"
          @renew="goRenew(sub)"
        />
        <GroupRateMultiplierList :subscription="walletSubscriptions[0]" />
      </div>

      <!-- 老 group 订阅 (v3) Grid -->
      <div v-else class="grid gap-6 lg:grid-cols-2">
        <div
          v-for="subscription in subscriptions"
          :key="subscription.id"
          class="overflow-hidden rounded-2xl border bg-white dark:bg-dark-800"
          :class="platformBorderClass(subscription.group?.platform || '')"
        >
          <!-- Header -->
          <div
            class="flex items-center justify-between border-b border-gray-100 p-4 dark:border-dark-700"
          >
            <div class="flex items-center gap-3">
              <div :class="['h-1.5 w-1.5 shrink-0 rounded-full', platformAccentDotClass(subscription.group?.platform || '')]" />
              <div>
                <div class="flex items-center gap-2">
                  <h3 class="font-semibold text-gray-900 dark:text-white">
                    {{ subscription.group?.name || `Group #${subscription.group_id}` }}
                  </h3>
                  <span :class="['rounded-md border px-2 py-0.5 text-[11px] font-medium', platformBadgeClass(subscription.group?.platform || '')]">
                    {{ platformLabel(subscription.group?.platform || '') }}
                  </span>
                </div>
                <p v-if="subscription.group?.description" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                  {{ subscription.group.description }}
                </p>
              </div>
            </div>
            <div class="flex items-center gap-2">
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
              <button
                v-if="subscription.status === 'active'"
                :class="['rounded-lg px-3 py-1.5 text-xs font-semibold text-white transition-colors', platformButtonClass(subscription.group?.platform || '')]"
                @click="openRenewModal(subscription)"
              >
                {{ t('payment.renewNow') }}
              </button>
            </div>
          </div>

          <!-- Usage Progress -->
          <div class="space-y-4 p-4">
            <!-- Expiration Info -->
            <div v-if="subscription.expires_at" class="flex items-center justify-between text-sm">
              <span class="text-gray-500 dark:text-dark-400">{{
                t('userSubscriptions.expires')
              }}</span>
              <span :class="getExpirationClass(subscription.expires_at)">
                {{ formatExpirationDate(subscription.expires_at) }}
              </span>
            </div>
            <div v-else class="flex items-center justify-between text-sm">
              <span class="text-gray-500 dark:text-dark-400">{{
                t('userSubscriptions.expires')
              }}</span>
              <span class="text-gray-700 dark:text-gray-300">{{
                t('userSubscriptions.noExpiration')
              }}</span>
            </div>

            <!-- Daily Usage -->
            <div v-if="subscription.group?.daily_limit_usd" class="space-y-2">
              <div class="flex items-center justify-between">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
                  {{ t('userSubscriptions.daily') }}
                </span>
                <span class="text-sm text-gray-500 dark:text-dark-400">
                  ${{ (subscription.daily_usage_usd || 0).toFixed(2) }} / ${{
                    subscription.group.daily_limit_usd.toFixed(2)
                  }}
                </span>
              </div>
              <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
                <div
                  class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
                  :class="
                    getProgressBarClass(
                      subscription.daily_usage_usd,
                      subscription.group.daily_limit_usd
                    )
                  "
                  :style="{
                    width: getProgressWidth(
                      subscription.daily_usage_usd,
                      subscription.group.daily_limit_usd
                    )
                  }"
                ></div>
              </div>
              <p
                v-if="subscription.daily_window_start"
                class="text-xs text-gray-500 dark:text-dark-400"
              >
                {{
                  t('userSubscriptions.resetIn', {
                    time: formatResetTime(subscription.daily_window_start, 24)
                  })
                }}
              </p>
            </div>

            <!-- Weekly Usage -->
            <div v-if="subscription.group?.weekly_limit_usd" class="space-y-2">
              <div class="flex items-center justify-between">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
                  {{ t('userSubscriptions.weekly') }}
                </span>
                <span class="text-sm text-gray-500 dark:text-dark-400">
                  ${{ (subscription.weekly_usage_usd || 0).toFixed(2) }} / ${{
                    subscription.group.weekly_limit_usd.toFixed(2)
                  }}
                </span>
              </div>
              <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
                <div
                  class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
                  :class="
                    getProgressBarClass(
                      subscription.weekly_usage_usd,
                      subscription.group.weekly_limit_usd
                    )
                  "
                  :style="{
                    width: getProgressWidth(
                      subscription.weekly_usage_usd,
                      subscription.group.weekly_limit_usd
                    )
                  }"
                ></div>
              </div>
              <p
                v-if="subscription.weekly_window_start"
                class="text-xs text-gray-500 dark:text-dark-400"
              >
                {{
                  t('userSubscriptions.resetIn', {
                    time: formatResetTime(subscription.weekly_window_start, 168)
                  })
                }}
              </p>
            </div>

            <!-- Monthly Usage -->
            <div v-if="subscription.group?.monthly_limit_usd" class="space-y-2">
              <div class="flex items-center justify-between">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
                  {{ t('userSubscriptions.monthly') }}
                </span>
                <span class="text-sm text-gray-500 dark:text-dark-400">
                  ${{ (subscription.monthly_usage_usd || 0).toFixed(2) }} / ${{
                    subscription.group.monthly_limit_usd.toFixed(2)
                  }}
                </span>
              </div>
              <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
                <div
                  class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
                  :class="
                    getProgressBarClass(
                      subscription.monthly_usage_usd,
                      subscription.group.monthly_limit_usd
                    )
                  "
                  :style="{
                    width: getProgressWidth(
                      subscription.monthly_usage_usd,
                      subscription.group.monthly_limit_usd
                    )
                  }"
                ></div>
              </div>
              <p
                v-if="subscription.monthly_window_start"
                class="text-xs text-gray-500 dark:text-dark-400"
              >
                {{
                  t('userSubscriptions.resetIn', {
                    time: formatResetTime(subscription.monthly_window_start, 720)
                  })
                }}
              </p>
            </div>

            <!-- No limits configured - Unlimited badge -->
            <div
              v-if="
                !subscription.group?.daily_limit_usd &&
                !subscription.group?.weekly_limit_usd &&
                !subscription.group?.monthly_limit_usd
              "
              class="flex items-center justify-center rounded-xl bg-gradient-to-r from-emerald-50 to-teal-50 py-6 dark:from-emerald-900/20 dark:to-teal-900/20"
            >
              <div class="flex items-center gap-3">
                <span class="text-4xl text-emerald-600 dark:text-emerald-400">∞</span>
                <div>
                  <p class="text-sm font-medium text-emerald-700 dark:text-emerald-300">
                    {{ t('userSubscriptions.unlimited') }}
                  </p>
                  <p class="text-xs text-emerald-600/70 dark:text-emerald-400/70">
                    {{ t('userSubscriptions.unlimitedDesc') }}
                  </p>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 续费 SKU 选择 modal — 链动小铺直跳,绕开内置 ZPay -->
    <div
      v-if="renewModalSub"
      class="fixed inset-0 z-[80] flex items-center justify-center bg-black/60 p-4"
      @click.self="closeRenewModal"
    >
      <div
        class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-2xl dark:bg-dark-800"
        @click.stop
      >
        <div class="mb-4 flex items-start justify-between">
          <div>
            <h2 class="text-lg font-bold text-gray-900 dark:text-white">选择续费 / 充值档位</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              点击下方任一档位将跳转链动小铺下单,完成后余额自动到账。
            </p>
          </div>
          <button
            class="rounded-lg p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700"
            @click="closeRenewModal"
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
              :class="renewRecommendedTier?.url === tier.url
                ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
                : 'border-gray-200 bg-white dark:bg-dark-800'"
              @click="openLiandong(tier.url)"
            >
              <div>
                <div class="flex items-center gap-2">
                  <span class="font-semibold text-gray-900 dark:text-white">{{ tier.name }}</span>
                  <span
                    v-if="renewRecommendedTier?.url === tier.url"
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
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAppStore } from '@/stores/app'
import subscriptionsAPI from '@/api/subscriptions'
import type { UserSubscription } from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import WalletBalanceCard from '@/components/user/WalletBalanceCard.vue'
import GroupRateMultiplierList from '@/components/user/GroupRateMultiplierList.vue'
import { formatDateOnly } from '@/utils/format'
import { platformBorderClass, platformBadgeClass, platformButtonClass, platformLabel } from '@/utils/platformColors'
import {
  LIANDONG_MONTHLY_TIERS,
  LIANDONG_CREDITS_TIERS,
  LIANDONG_CUSTOM_WECHAT,
  matchMonthlyTier
} from '@/constants/liandongSku'

function platformAccentDotClass(p: string): string {
  switch (p) {
    case 'anthropic': return 'bg-orange-500'
    case 'openai': return 'bg-emerald-500'
    case 'antigravity': return 'bg-purple-500'
    case 'gemini': return 'bg-blue-500'
    default: return 'bg-gray-400'
  }
}

const { t } = useI18n()
const router = useRouter()
const appStore = useAppStore()

const subscriptions = ref<UserSubscription[]>([])

// 续费 SKU 选择 modal — 内置 ZPay 没开通,所有续费/充值统一跳链动小铺 SKU
const renewModalSub = ref<UserSubscription | null>(null)
const renewRecommendedTier = computed(() => {
  const sub = renewModalSub.value
  if (!sub) return null
  return matchMonthlyTier(sub.group?.monthly_limit_usd)
})

function openRenewModal(sub: UserSubscription) {
  renewModalSub.value = sub
}

function closeRenewModal() {
  renewModalSub.value = null
}

function openLiandong(url: string) {
  window.open(url, '_blank', 'noopener')
  closeRenewModal()
}

function copyCustomWechat() {
  navigator.clipboard?.writeText(LIANDONG_CUSTOM_WECHAT).catch(() => {})
  appStore.showSuccess?.(`已复制微信号 ${LIANDONG_CUSTOM_WECHAT}`)
}
const loading = ref(true)

// 钱包模式订阅（v4）：wallet_balance_usd 非空。与老 group 订阅互斥，
// 只要存在任意一条钱包订阅，整页就切到钱包视图。
const walletSubscriptions = computed(() =>
  subscriptions.value.filter((s) => s.wallet_balance_usd != null)
)

function goRenew(sub: UserSubscription) {
  router.push({ path: '/purchase', query: { tab: 'subscription', wallet: '1', sub: String(sub.id) } })
}

async function loadSubscriptions() {
  try {
    loading.value = true
    subscriptions.value = await subscriptionsAPI.getMySubscriptions()
  } catch (error) {
    console.error('Failed to load subscriptions:', error)
    appStore.showError(t('userSubscriptions.failedToLoad'))
  } finally {
    loading.value = false
  }
}

function getProgressWidth(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return '0%'
  const percentage = Math.min(((used || 0) / limit) * 100, 100)
  return `${percentage}%`
}

function getProgressBarClass(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return 'bg-gray-400'
  const percentage = ((used || 0) / limit) * 100
  if (percentage >= 90) return 'bg-red-500'
  if (percentage >= 70) return 'bg-orange-500'
  return 'bg-green-500'
}

function formatExpirationDate(expiresAt: string): string {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  const days = Math.ceil(diff / (1000 * 60 * 60 * 24))

  if (days < 0) {
    return t('userSubscriptions.status.expired')
  }

  const dateStr = formatDateOnly(expires)

  if (days === 0) {
    return `${dateStr} (${t('common.today')})`
  }
  if (days === 1) {
    return `${dateStr} (${t('common.tomorrow')})`
  }

  return t('userSubscriptions.daysRemaining', { days }) + ` (${dateStr})`
}

function getExpirationClass(expiresAt: string): string {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  const days = Math.ceil(diff / (1000 * 60 * 60 * 24))

  if (days <= 0) return 'text-red-600 dark:text-red-400 font-medium'
  if (days <= 3) return 'text-red-600 dark:text-red-400'
  if (days <= 7) return 'text-orange-600 dark:text-orange-400'
  return 'text-gray-700 dark:text-gray-300'
}

function formatResetTime(windowStart: string | null, windowHours: number): string {
  if (!windowStart) return t('userSubscriptions.windowNotActive')

  const start = new Date(windowStart)
  const end = new Date(start.getTime() + windowHours * 60 * 60 * 1000)
  const now = new Date()
  const diff = end.getTime() - now.getTime()

  if (diff <= 0) return t('userSubscriptions.windowNotActive')

  const hours = Math.floor(diff / (1000 * 60 * 60))
  const minutes = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60))

  if (hours > 24) {
    const days = Math.floor(hours / 24)
    const remainingHours = hours % 24
    return `${days}d ${remainingHours}h`
  }

  if (hours > 0) {
    return `${hours}h ${minutes}m`
  }

  return `${minutes}m`
}

onMounted(() => {
  loadSubscriptions()
})
</script>
