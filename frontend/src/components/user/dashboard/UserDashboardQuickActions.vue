<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('dashboard.quickActions') }}</h2>
    </div>
    <div class="space-y-3 p-4">
      <div
        class="rounded-xl bg-gray-50 p-4 transition-all duration-200 dark:bg-dark-800/50"
      >
        <div class="flex items-center gap-4">
          <div
            class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl"
            :class="selectedLaunchMode === 'image'
              ? 'bg-fuchsia-100 dark:bg-fuchsia-900/30'
              : 'bg-sky-100 dark:bg-sky-900/30'"
          >
            <Icon
              :name="selectedLaunchMode === 'image' ? 'sparkles' : 'chat'"
              size="lg"
              :class="selectedLaunchMode === 'image'
                ? 'text-fuchsia-600 dark:text-fuchsia-400'
                : 'text-sky-600 dark:text-sky-400'"
            />
          </div>
          <div class="min-w-0 flex-1">
            <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.openChat') }}</p>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.openChatHint') }}</p>
          </div>
          <button
            type="button"
            @click="openChat"
            :disabled="openingChat"
            class="inline-flex flex-shrink-0 items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-white transition-colors disabled:cursor-wait disabled:opacity-70"
            :class="selectedLaunchMode === 'image'
              ? 'bg-fuchsia-600 hover:bg-fuchsia-700 dark:bg-fuchsia-500 dark:hover:bg-fuchsia-600'
              : 'bg-sky-600 hover:bg-sky-700 dark:bg-sky-500 dark:hover:bg-sky-600'"
          >
            <span>{{ openingChat ? t('dashboard.chatOpening') : t('dashboard.chatOpenButton') }}</span>
            <Icon name="externalLink" size="sm" />
          </button>
        </div>
        <div class="mt-4">
          <p class="text-xs font-medium text-gray-600 dark:text-dark-300">
            {{ t('dashboard.chatModeLabel') }}
          </p>
          <div class="mt-2 grid grid-cols-2 gap-2 rounded-lg bg-white p-1 dark:bg-dark-900">
            <button
              type="button"
              data-test-id="launch-mode-chat"
              :aria-pressed="selectedLaunchMode === 'chat'"
              :disabled="openingChat"
              class="rounded-md px-3 py-2 text-sm font-medium transition-colors disabled:cursor-wait disabled:opacity-70"
              :class="selectedLaunchMode === 'chat'
                ? 'bg-sky-600 text-white shadow-sm dark:bg-sky-500'
                : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-800'"
              @click="selectedLaunchMode = 'chat'"
            >
              {{ t('dashboard.chatModeChat') }}
            </button>
            <button
              type="button"
              data-test-id="launch-mode-image"
              :aria-pressed="selectedLaunchMode === 'image'"
              :disabled="openingChat"
              class="rounded-md px-3 py-2 text-sm font-medium transition-colors disabled:cursor-wait disabled:opacity-70"
              :class="selectedLaunchMode === 'image'
                ? 'bg-fuchsia-600 text-white shadow-sm dark:bg-fuchsia-500'
                : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-800'"
              @click="selectedLaunchMode = 'image'"
            >
              {{ t('dashboard.chatModeImage') }}
            </button>
          </div>
        </div>
        <label class="mt-4 block text-xs font-medium text-gray-600 dark:text-dark-300" for="launch-model-select">
          {{ selectedLaunchMode === 'image' ? t('dashboard.imageModelLabel') : t('dashboard.chatModelLabel') }}
        </label>
        <select
          id="launch-model-select"
          v-model="selectedLaunchModel"
          :disabled="openingChat"
          class="mt-2 w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 outline-none transition-colors focus:border-sky-500 focus:ring-2 focus:ring-sky-100 disabled:cursor-wait disabled:opacity-70 dark:border-dark-600 dark:bg-dark-900 dark:text-white dark:focus:border-sky-400 dark:focus:ring-sky-900/40"
        >
          <option v-for="choice in launchModelChoices" :key="choice.model" :value="choice.model">
            {{ choice.label }} · {{ getChatModelTagline(choice) }}
          </option>
        </select>
      </div>

      <button @click="router.push('/keys')" class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 dark:bg-dark-800/50 dark:hover:bg-dark-800">
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl bg-primary-100 transition-transform group-hover:scale-105 dark:bg-primary-900/30">
          <Icon name="key" size="lg" class="text-primary-600 dark:text-primary-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.createApiKey') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.generateNewKey') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-primary-500 dark:text-dark-500"
        />
      </button>

      <button @click="router.push('/usage')" class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 dark:bg-dark-800/50 dark:hover:bg-dark-800">
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl bg-emerald-100 transition-transform group-hover:scale-105 dark:bg-emerald-900/30">
          <Icon name="chart" size="lg" class="text-emerald-600 dark:text-emerald-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.viewUsage') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.checkDetailedLogs') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-emerald-500 dark:text-dark-500"
        />
      </button>

      <button @click="router.push('/redeem')" class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 dark:bg-dark-800/50 dark:hover:bg-dark-800">
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl bg-amber-100 transition-transform group-hover:scale-105 dark:bg-amber-900/30">
          <Icon name="gift" size="lg" class="text-amber-600 dark:text-amber-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.redeemCode') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.addBalanceWithCode') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-amber-500 dark:text-dark-500"
        />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { authAPI } from '@/api'
import Icon from '@/components/icons/Icon.vue'
import {
  buildImageFallbackURL,
  buildImageRedirectPath,
  buildChatFallbackURL,
  buildChatRedirectPath,
  chatModelChoices,
  defaultChatModelChoice,
  defaultImageModelChoice,
  imageModelChoices,
  type ChatModelChoice
} from './chatModelPresets'
const router = useRouter()
const { t } = useI18n()
const openingChat = ref(false)
const selectedLaunchMode = ref<'chat' | 'image'>('chat')
const selectedChatModel = ref(defaultChatModelChoice.model)
const selectedImageModel = ref(defaultImageModelChoice.model)

const launchModelChoices = computed(() =>
  selectedLaunchMode.value === 'image' ? imageModelChoices : chatModelChoices
)

const selectedLaunchModel = computed({
  get: () => (selectedLaunchMode.value === 'image' ? selectedImageModel.value : selectedChatModel.value),
  set: (model: string) => {
    if (selectedLaunchMode.value === 'image') {
      selectedImageModel.value = model
      return
    }
    selectedChatModel.value = model
  }
})

const selectedChatModelChoice = computed(
  () => chatModelChoices.find((choice) => choice.model === selectedChatModel.value) || defaultChatModelChoice
)

const selectedImageModelChoice = computed(
  () => imageModelChoices.find((choice) => choice.model === selectedImageModel.value) || defaultImageModelChoice
)

const selectedLaunchModelChoice = computed(() =>
  selectedLaunchMode.value === 'image' ? selectedImageModelChoice.value : selectedChatModelChoice.value
)

const getChatModelTagline = (choice: ChatModelChoice) =>
  choice.taglineKey ? t(choice.taglineKey) : choice.tagline

const openChat = async () => {
  if (openingChat.value) return
  openingChat.value = true
  try {
    const choice = selectedLaunchModelChoice.value
    const isImageLaunch = selectedLaunchMode.value === 'image'
    const redirectPath = isImageLaunch ? buildImageRedirectPath(choice) : buildChatRedirectPath(choice)
    const result = await authAPI.createChatBridgeCode({ redirect_path: redirectPath })
    window.location.href = result.chat_url || (
      isImageLaunch ? buildImageFallbackURL(result.code, choice) : buildChatFallbackURL(result.code, choice)
    )
  } finally {
    openingChat.value = false
  }
}
</script>
