<template>
  <div class="overflow-hidden rounded-2xl border bg-white dark:border-dark-700 dark:bg-dark-800">
    <div class="border-b border-gray-100 p-4 dark:border-dark-700">
      <h3 class="font-semibold text-gray-900 dark:text-white">
        {{ t('userSubscriptions.wallet.rateListTitle') }}
      </h3>
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
        {{ t('userSubscriptions.wallet.rateListDesc') }}
      </p>
    </div>
    <div v-if="loading" class="flex items-center justify-center py-8">
      <div class="h-6 w-6 animate-spin rounded-full border-2 border-amber-500 border-t-transparent"></div>
    </div>
    <div v-else-if="rows.length === 0" class="p-6 text-center text-sm text-gray-500 dark:text-dark-400">
      {{ t('userSubscriptions.wallet.rateListEmpty') }}
    </div>
    <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
      <li
        v-for="row in rows"
        :key="row.id"
        class="flex items-center justify-between px-4 py-3"
      >
        <div class="flex min-w-0 items-center gap-3">
          <div :class="['h-1.5 w-1.5 shrink-0 rounded-full', dotClass(row.platform)]"></div>
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="truncate font-medium text-gray-900 dark:text-white">
                {{ row.name }}
              </span>
              <span :class="['rounded-md border px-1.5 py-0.5 text-[10px] font-medium', platformBadgeClass(row.platform || '')]">
                {{ platformLabel(row.platform || '') }}
              </span>
            </div>
            <p v-if="row.description" class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">
              {{ row.description }}
            </p>
          </div>
        </div>
        <div class="ml-3 flex shrink-0 items-center gap-2">
          <span
            :class="[
              'rounded-lg px-2 py-1 text-sm font-semibold tabular-nums',
              multiplierClass(row.effectiveMultiplier)
            ]"
          >
            ×{{ row.effectiveMultiplier.toFixed(2) }}
          </span>
          <span
            v-if="row.overridden"
            class="rounded-md bg-blue-50 px-1.5 py-0.5 text-[10px] font-medium text-blue-700 dark:bg-blue-900/30 dark:text-blue-300"
            :title="t('userSubscriptions.wallet.userOverrideHint', { base: row.baseMultiplier.toFixed(2) })"
          >
            {{ t('userSubscriptions.wallet.userOverride') }}
          </span>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import userGroupsAPI from '@/api/groups'
import type { Group } from '@/types'
import { platformBadgeClass, platformLabel } from '@/utils/platformColors'

const { t } = useI18n()

const groups = ref<Group[]>([])
const userRates = ref<Record<number, number>>({})
const loading = ref(true)

interface Row {
  id: number
  name: string
  description: string | null
  platform: Group['platform']
  baseMultiplier: number
  effectiveMultiplier: number
  overridden: boolean
}

const rows = computed<Row[]>(() =>
  groups.value
    .filter((g) => g.status === 'active')
    .map((g) => {
      const override = userRates.value[g.id]
      const overridden = override != null && override !== g.rate_multiplier
      return {
        id: g.id,
        name: g.name,
        description: g.description,
        platform: g.platform,
        baseMultiplier: g.rate_multiplier,
        effectiveMultiplier: overridden ? override : g.rate_multiplier,
        overridden
      }
    })
    .sort((a, b) => a.effectiveMultiplier - b.effectiveMultiplier)
)

function dotClass(p: string): string {
  switch (p) {
    case 'anthropic': return 'bg-orange-500'
    case 'openai': return 'bg-emerald-500'
    case 'antigravity': return 'bg-purple-500'
    case 'gemini': return 'bg-blue-500'
    default: return 'bg-gray-400'
  }
}

function multiplierClass(m: number): string {
  if (m >= 2) return 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (m >= 1.5) return 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (m === 0) return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  return 'bg-gray-50 text-gray-700 dark:bg-dark-700 dark:text-gray-200'
}

onMounted(async () => {
  try {
    const [g, r] = await Promise.all([
      userGroupsAPI.getAvailable(),
      userGroupsAPI.getUserGroupRates()
    ])
    groups.value = g
    userRates.value = r
  } catch (err) {
    console.error('Failed to load groups for rate list:', err)
  } finally {
    loading.value = false
  }
})
</script>
