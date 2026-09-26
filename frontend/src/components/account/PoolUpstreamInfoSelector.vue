<template>
  <div class="space-y-3" data-testid="pool-upstream-info-selector">
    <div>
      <label class="input-label">{{ t('admin.accounts.poolUpstream.platform') }}</label>
      <select
        class="input"
        data-testid="pool-upstream-platform"
        :value="platform"
        @change="onPlatformChange(($event.target as HTMLSelectElement).value)"
      >
        <option
          v-for="option in platformOptions"
          :key="option.value"
          :value="option.value"
          :disabled="option.disabled"
        >
          {{ option.label }}
        </option>
      </select>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.poolUpstream.platformHint') }}
      </p>
    </div>
    <div v-if="platform !== 'default'" data-testid="pool-upstream-features">
      <label class="input-label">{{ t('admin.accounts.poolUpstream.features') }}</label>
      <label
        v-for="feature in supportedFeatures"
        :key="feature"
        class="flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm text-gray-700 hover:bg-gray-50 dark:text-dark-300 dark:hover:bg-dark-700/40"
      >
        <input
          type="checkbox"
          class="rounded border-gray-300 dark:border-dark-600"
          :value="feature"
          :checked="features.includes(feature)"
          :data-testid="`pool-upstream-feature-${feature}`"
          @change="onFeatureToggle(feature, ($event.target as HTMLInputElement).checked)"
        />
        {{ featureLabel(feature) }}
      </label>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.poolUpstream.featuresHint') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PoolUpstreamPlatform } from '@/types'

const props = defineProps<{
  platform: string
  features: string[]
  // Provider API platform of the account (not the declared pool upstream);
  // chatgpt2api is only valid for openai API-key accounts.
  accountPlatform: string
}>()

const emit = defineEmits<{
  (event: 'update:platform', value: PoolUpstreamPlatform): void
  (event: 'update:features', value: string[]): void
}>()

const { t } = useI18n()

const SUPPORTED_FEATURES: Record<string, string[]> = {
  sub2api: ['balance'],
  chatgpt2api: ['account_count', 'image_quota']
}

const platformOptions = computed(() => [
  { value: 'default', label: t('admin.accounts.poolUpstream.platformDefault'), disabled: false },
  { value: 'sub2api', label: t('admin.accounts.poolUpstream.platformSub2api'), disabled: false },
  {
    value: 'chatgpt2api',
    label: t('admin.accounts.poolUpstream.platformChatgpt2api'),
    disabled: props.accountPlatform !== 'openai'
  }
])

const supportedFeatures = computed(() => SUPPORTED_FEATURES[props.platform] ?? [])

// chatgpt2api is only valid for OpenAI accounts: when the form's provider
// changes away from OpenAI, clear the selection instead of relying on the
// backend to reject the combination.
watch(
  () => props.accountPlatform,
  accountPlatform => {
    if (props.platform === 'chatgpt2api' && accountPlatform !== 'openai') {
      emit('update:platform', 'default')
      emit('update:features', [])
    }
  }
)

const onPlatformChange = (value: string) => {
  const platform = value === 'sub2api' || value === 'chatgpt2api' ? value : 'default'
  emit('update:platform', platform)
  // Switching platform clears incompatible choices.
  const supported = SUPPORTED_FEATURES[platform] ?? []
  emit('update:features', props.features.filter(feature => supported.includes(feature)))
}

const onFeatureToggle = (feature: string, checked: boolean) => {
  const next = checked
    ? [...props.features, feature]
    : props.features.filter(item => item !== feature)
  emit('update:features', next)
}

const featureLabel = (feature: string) =>
  t(`admin.accounts.poolUpstream.feature_${feature}`)
</script>
