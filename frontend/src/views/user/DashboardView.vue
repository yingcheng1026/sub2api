<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex items-center justify-center py-12"><LoadingSpinner /></div>
      <template v-else-if="stats">
        <div v-if="walletSubscription" class="max-w-3xl space-y-4">
          <WalletBalanceCard
            :subscription="walletSubscription"
            @renew="goRenew(walletSubscription)"
          />
          <WalletModelRouteList />
        </div>
        <div
          v-if="walletLookupFailed"
          data-hfc-wallet-status-unavailable
          class="rounded-xl border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-200"
        >
          {{ t('dashboard.walletStatusUnavailable') }}
        </div>
        <UserDashboardStats
          :stats="stats"
          :balance="user?.balance || 0"
          :is-simple="authStore.isSimpleMode"
          :hide-legacy-balance="hideLegacyBalance"
        />
        <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>
    <RenewLiandongModal
      :show="renewModalSub !== null"
      @close="closeRenewModal"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import subscriptionsAPI from '@/api/subscriptions'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import WalletBalanceCard from '@/components/user/WalletBalanceCard.vue'
import WalletModelRouteList from '@/components/user/WalletModelRouteList.vue'
import RenewLiandongModal from '@/components/user/RenewLiandongModal.vue'
import type { UsageLog, TrendDataPoint, ModelStat, UserSubscription } from '@/types'
import { isActiveCreditsWallet } from '@/utils/subscriptionWallet'

const authStore = useAuthStore()
const { t } = useI18n()
const user = computed(() => authStore.user)
const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const walletSubscription = ref<UserSubscription | null>(null)
const walletLoading = ref(true)
const walletLookupFailed = ref(false)
const hideLegacyBalance = computed(
  () => walletLoading.value || walletLookupFailed.value || walletSubscription.value !== null
)
const renewModalSub = ref<UserSubscription | null>(null)

const formatLD = (d: Date) => d.toISOString().split('T')[0]
const startDate = ref(formatLD(new Date(Date.now() - 6 * 86400000)))
const endDate = ref(formatLD(new Date()))
const granularity = ref('day')

const loadStats = async () => {
  loading.value = true
  try {
    await authStore.refreshUser()
    stats.value = await usageAPI.getDashboardStats()
  } catch (error) {
    console.error('Failed to load dashboard stats:', error)
  } finally {
    loading.value = false
  }
}

const loadCharts = async () => {
  loadingCharts.value = true
  try {
    const res = await Promise.all([
      usageAPI.getDashboardTrend({
        start_date: startDate.value,
        end_date: endDate.value,
        granularity: granularity.value as any
      }),
      usageAPI.getDashboardModels({ start_date: startDate.value, end_date: endDate.value })
    ])
    trendData.value = res[0].trend || []
    modelStats.value = res[1].models || []
  } catch (error) {
    console.error('Failed to load charts:', error)
  } finally {
    loadingCharts.value = false
  }
}

const loadRecent = async () => {
  loadingUsage.value = true
  try {
    const res = await usageAPI.getByDateRange(startDate.value, endDate.value)
    recentUsage.value = res.items.slice(0, 5)
  } catch (error) {
    console.error('Failed to load recent usage:', error)
  } finally {
    loadingUsage.value = false
  }
}

const loadWalletSubscription = async () => {
  walletLoading.value = true
  walletLookupFailed.value = false
  try {
    const subscriptions = await subscriptionsAPI.getActiveSubscriptions()
    walletSubscription.value = subscriptions.find(isActiveCreditsWallet) || null
  } catch (error) {
    console.error('Failed to load wallet subscription:', error)
    walletSubscription.value = null
    walletLookupFailed.value = true
  } finally {
    walletLoading.value = false
  }
}

const goRenew = (sub: UserSubscription) => {
  renewModalSub.value = sub
}

const closeRenewModal = () => {
  renewModalSub.value = null
}

const refreshAll = () => {
  loadStats()
  loadCharts()
  loadRecent()
  loadWalletSubscription()
}

onMounted(() => { refreshAll() })
</script>
