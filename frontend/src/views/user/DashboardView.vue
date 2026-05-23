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

        <!-- 余额充值 CTA — 非简易模式 & 无 wallet 订阅时显示 (wallet 用户已有 WalletBalanceCard 续费按钮) -->
        <div
          v-if="!authStore.isSimpleMode && !walletSubscription"
          class="card flex flex-col items-start gap-3 p-5 sm:flex-row sm:items-center sm:justify-between"
        >
          <div class="flex items-center gap-4">
            <div class="rounded-xl bg-emerald-100 p-3 dark:bg-emerald-900/30">
              <svg class="h-6 w-6 text-emerald-600 dark:text-emerald-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 10h18M5 6h14a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2z"/>
              </svg>
            </div>
            <div>
              <p class="text-xs font-medium text-gray-500 dark:text-gray-400">当前余额</p>
              <p class="text-2xl font-bold text-emerald-600 dark:text-emerald-400">${{ (user?.balance || 0).toFixed(2) }}</p>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">永久有效 · 所有模型可用 · 按渠道倍率扣费</p>
            </div>
          </div>
          <div class="flex gap-2">
            <button
              type="button"
              class="rounded-lg bg-emerald-600 px-5 py-2.5 text-sm font-semibold text-white shadow-sm transition-all hover:bg-emerald-700"
              @click="openRechargeModal"
            >
              立即充值 / 续费
            </button>
          </div>
        </div>

        <UserDashboardStats :stats="stats" :balance="user?.balance || 0" :is-simple="authStore.isSimpleMode" />
        <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>

    <!-- 续费 / 充值 modal — 共享组件,跟 SubscriptionsView 同一份 -->
    <RenewLiandongModal
      :show="rechargeModalSub != null || rechargeModalOpen"
      :subscription="rechargeModalSub"
      @close="closeRechargeModal"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
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

const authStore = useAuthStore()
const user = computed(() => authStore.user)
const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const walletSubscription = ref<UserSubscription | null>(null)

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
  try {
    const subscriptions = await subscriptionsAPI.getActiveSubscriptions()
    walletSubscription.value = subscriptions.find((sub) =>
      sub.status === 'active' && sub.wallet_balance_usd != null
    ) || null
  } catch (error) {
    console.error('Failed to load wallet subscription:', error)
    walletSubscription.value = null
  }
}

// 续费 / 充值 modal — wallet 用户从 WalletBalanceCard 的「续费」按钮触发,
// 非 wallet 用户从余额 CTA 卡片触发 (rechargeModalOpen=true 但 sub=null,modal 不高亮"同档")
const rechargeModalSub = ref<UserSubscription | null>(null)
const rechargeModalOpen = ref(false)

const goRenew = (sub: UserSubscription) => {
  rechargeModalSub.value = sub
  rechargeModalOpen.value = true
}

const openRechargeModal = () => {
  rechargeModalSub.value = null
  rechargeModalOpen.value = true
}

const closeRechargeModal = () => {
  rechargeModalSub.value = null
  rechargeModalOpen.value = false
}

const refreshAll = () => {
  loadStats()
  loadCharts()
  loadRecent()
  loadWalletSubscription()
}

onMounted(() => { refreshAll() })
</script>
