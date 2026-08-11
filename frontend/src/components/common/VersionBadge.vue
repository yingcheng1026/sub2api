<template>
  <div class="relative">
    <template v-if="isAdmin">
      <button
        class="flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs transition-colors"
        :class="
          hasUpdate
            ? 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400 dark:hover:bg-amber-900/50'
            : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-800 dark:text-dark-400 dark:hover:bg-dark-700'
        "
        :title="hasUpdate ? t('version.updateAvailable') : t('version.upToDate')"
        @click="toggleDropdown"
      >
        <span v-if="currentVersion" class="font-medium">v{{ currentVersion }}</span>
        <span v-else class="h-3 w-12 animate-pulse rounded bg-gray-200 dark:bg-dark-600" />
        <span v-if="hasUpdate" class="relative flex h-2 w-2">
          <span
            class="absolute inline-flex h-full w-full animate-ping rounded-full bg-amber-400 opacity-75"
          />
          <span class="relative inline-flex h-2 w-2 rounded-full bg-amber-500" />
        </span>
      </button>

      <transition name="dropdown">
        <div
          v-if="dropdownOpen"
          ref="dropdownRef"
          class="absolute left-0 z-50 mt-2 w-64 overflow-hidden whitespace-normal rounded-xl border border-gray-200 bg-white shadow-lg dark:border-dark-700 dark:bg-dark-800"
        >
          <div
            class="flex items-center justify-between border-b border-gray-100 px-4 py-3 dark:border-dark-700"
          >
            <span class="text-sm font-medium text-gray-700 dark:text-dark-300">{{
              t('version.currentVersion')
            }}</span>
            <button
              class="rounded-lg p-1.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700 dark:hover:text-dark-200"
              :disabled="loading"
              :title="t('version.refresh')"
              @click="refreshVersion"
            >
              <Icon
                name="refresh"
                size="sm"
                :stroke-width="2"
                :class="{ 'animate-spin': loading }"
              />
            </button>
          </div>

          <div class="space-y-3 p-4">
            <div class="text-center">
              <span v-if="currentVersion" class="text-2xl font-bold text-gray-900 dark:text-white">
                v{{ currentVersion }}
              </span>
              <span v-else class="text-2xl font-bold text-gray-400 dark:text-dark-500">--</span>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{
                  hasUpdate
                    ? `${t('version.latestVersion')}: v${latestVersion}`
                    : t('version.upToDate')
                }}
              </p>
            </div>

            <div
              v-if="hasUpdate"
              class="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800/50 dark:bg-amber-900/20"
            >
              <p class="text-sm font-medium text-amber-700 dark:text-amber-300">
                {{ t('version.updateAvailable') }}
              </p>
              <p class="mt-1 text-xs leading-5 text-amber-600/80 dark:text-amber-400/80">
                {{ t('version.immutableDeploymentHint') }}
              </p>
            </div>

            <a
              v-if="releaseInfo?.html_url && releaseInfo.html_url !== '#'"
              :href="releaseInfo.html_url"
              target="_blank"
              rel="noopener noreferrer"
              class="flex items-center justify-center gap-1 text-xs text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-dark-200"
            >
              {{ hasUpdate ? t('version.viewChangelog') : t('version.viewRelease') }}
              <Icon name="externalLink" size="xs" :stroke-width="2" />
            </a>
          </div>
        </div>
      </transition>
    </template>

    <span v-else-if="version" class="text-xs text-gray-500 dark:text-dark-400">
      v{{ version }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore, useAuthStore } from '@/stores'

const props = defineProps<{
  version?: string
}>()

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()

const dropdownOpen = ref(false)
const dropdownRef = ref<HTMLElement | null>(null)

const isAdmin = computed(() => authStore.isAdmin)
const loading = computed(() => appStore.versionLoading)
const currentVersion = computed(() => appStore.currentVersion || props.version || '')
const latestVersion = computed(() => appStore.latestVersion)
const hasUpdate = computed(() => appStore.hasUpdate)
const releaseInfo = computed(() => appStore.releaseInfo)

function toggleDropdown() {
  dropdownOpen.value = !dropdownOpen.value
}

async function refreshVersion() {
  if (!isAdmin.value || loading.value) return
  await appStore.fetchVersion(true)
}

function handleClickOutside(event: MouseEvent) {
  const target = event.target as Node
  const button = (event.target as Element).closest('button')
  if (dropdownRef.value && !dropdownRef.value.contains(target) && !button?.contains(target)) {
    dropdownOpen.value = false
  }
}

onMounted(() => {
  if (isAdmin.value) {
    void appStore.fetchVersion(false)
  }
  document.addEventListener('click', handleClickOutside)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>

<style scoped>
.dropdown-enter-active,
.dropdown-leave-active {
  transition: all 0.2s ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: scale(0.95) translateY(-4px);
}
</style>
