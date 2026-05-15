<template>
  <div class="overflow-hidden rounded-2xl border bg-white dark:border-dark-700 dark:bg-dark-800">
    <div class="border-b border-gray-100 p-4 dark:border-dark-700">
      <h3 class="font-semibold text-gray-900 dark:text-white">
        {{ t('userSubscriptions.wallet.routeListTitle') }}
      </h3>
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
        {{ t('userSubscriptions.wallet.routeListDesc') }}
      </p>
    </div>

    <div v-if="loading" class="flex items-center justify-center py-7">
      <div class="h-5 w-5 animate-spin rounded-full border-2 border-amber-500 border-t-transparent"></div>
    </div>

    <div v-else-if="rows.length === 0" class="p-5 text-sm text-gray-500 dark:text-dark-400">
      {{ t('userSubscriptions.wallet.routeListEmpty') }}
    </div>

    <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
      <li
        v-for="row in rows"
        :key="`${row.pattern}:${row.group_id}`"
        class="flex items-center justify-between gap-3 px-4 py-3"
      >
        <div class="min-w-0">
          <div class="truncate text-sm font-medium text-gray-900 dark:text-white">
            {{ t('userSubscriptions.wallet.routeModel', { model: displayModel(row) }) }}
          </div>
          <div class="mt-1 flex items-center gap-2">
            <span :class="['h-1.5 w-1.5 shrink-0 rounded-full', dotClass(row.platform)]"></span>
            <span class="truncate text-xs text-gray-500 dark:text-dark-400">
              {{ row.group_name }}
            </span>
          </div>
        </div>

        <div class="flex shrink-0 items-center gap-2">
          <span :class="['rounded-md border px-1.5 py-0.5 text-[10px] font-medium', platformBadgeClass(row.platform || '')]">
            {{ platformLabel(row.platform || '') }}
          </span>
          <span class="rounded-lg bg-amber-50 px-2 py-1 text-sm font-semibold tabular-nums text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">
            ×{{ row.effective_rate_multiplier.toFixed(2) }}
          </span>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import userGroupsAPI, { type WalletModelRoute } from '@/api/groups'
import { platformBadgeClass, platformLabel } from '@/utils/platformColors'

const { t } = useI18n()

const routes = ref<WalletModelRoute[]>([])
const loading = ref(true)

const rows = computed(() =>
  routes.value.filter((row) => row.example_model || row.pattern)
)

function displayModel(row: WalletModelRoute): string {
  return row.example_model || row.pattern
}

function dotClass(platform: string): string {
  switch (platform) {
    case 'anthropic': return 'bg-orange-500'
    case 'openai': return 'bg-emerald-500'
    case 'antigravity': return 'bg-purple-500'
    case 'gemini': return 'bg-blue-500'
    default: return 'bg-gray-400'
  }
}

onMounted(async () => {
  try {
    routes.value = await userGroupsAPI.getWalletModelRoutes()
  } catch (error) {
    console.error('Failed to load wallet model routes:', error)
  } finally {
    loading.value = false
  }
})
</script>
