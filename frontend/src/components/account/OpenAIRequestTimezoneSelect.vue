<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <div class="flex items-center justify-between gap-4">
      <div>
        <label class="input-label mb-0">{{ t('admin.codex.timezone.label') }}</label>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.codex.timezone.hint', { timezone: defaultTimezone }) }}
        </p>
        <p v-if="error" class="mt-1 text-xs text-red-500 dark:text-red-400">
          {{ t('admin.codex.timezone.loadFailed') }}
        </p>
      </div>
      <div class="w-60">
        <Select
          :model-value="modelValue"
          :options="options"
          :disabled="disabled || loading"
          :loading="loading"
          :searchable="true"
          :placeholder="t('admin.codex.timezone.placeholder')"
          @update:model-value="handleUpdate"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { useOpenAIRequestTimezones } from '@/composables/useOpenAIRequestTimezones'
import { buildOpenAIRequestTimezoneOptions } from './openaiRequestTimezone'

withDefaults(
  defineProps<{
    modelValue: string
    disabled?: boolean
  }>(),
  { disabled: false }
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()
const { t } = useI18n()
const { defaultTimezone, timezones, loading, error, load } = useOpenAIRequestTimezones()

const options = computed(() =>
  buildOpenAIRequestTimezoneOptions(
    defaultTimezone.value,
    timezones.value,
    t('admin.codex.timezone.followDefault')
  )
)

const handleUpdate = (value: string | number | boolean | null) => {
  emit('update:modelValue', typeof value === 'string' ? value : '')
}

onMounted(() => {
  void load()
})
</script>
