<template>
  <BaseDialog
    :show="show"
    :title="t('admin.promptTemplates.preview.title')"
    width="extra-wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.promptTemplates.preview.description') }}
      </p>

      <div class="space-y-2">
        <p
          role="status"
          class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-2.5 text-sm text-sky-900 dark:border-sky-900/70 dark:bg-sky-950/30 dark:text-sky-200"
        >
          {{ t('admin.promptTemplates.preview.structuralNotice') }}
        </p>
        <p
          role="status"
          class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-2.5 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
        >
          {{ t('admin.promptTemplates.preview.behaviorNotice') }}
        </p>
      </div>

      <div v-if="loading" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.promptTemplates.preview.running') }}
      </div>

      <p v-else-if="error" role="alert" class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
        {{ error }}
      </p>

      <template v-else-if="result">
        <dl class="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
            <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.version') }}</dt>
            <dd class="mt-0.5 font-medium text-gray-900 dark:text-white">{{ versionLabel(version) || `#${result.version_id}` }}</dd>
          </div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
            <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.bodyBytes') }}</dt>
            <dd class="mt-0.5 font-mono text-gray-900 dark:text-white">{{ result.body_bytes }}</dd>
          </div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
            <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.addedBytes') }}</dt>
            <dd class="mt-0.5 font-mono text-gray-900 dark:text-white">{{ result.added_bytes }}</dd>
          </div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
            <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.manifestSha256') }}</dt>
            <dd class="mt-0.5 break-all font-mono text-xs text-gray-900 dark:text-white" :title="result.manifest_sha256">
              {{ shortenSha(result.manifest_sha256) }}
            </dd>
          </div>
        </dl>

        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-600">
          <table class="w-full min-w-max text-sm">
            <thead class="bg-gray-50 dark:bg-dark-700">
              <tr>
                <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.profile') }}</th>
                <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.field') }}</th>
                <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.supported') }}</th>
                <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.originalValue') }}</th>
                <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.preview.injectedValue') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-600">
              <tr v-for="item in result.profile_previews" :key="item.profile">
                <td class="px-3 py-2 align-top">
                  <div class="text-gray-900 dark:text-white">{{ t(`admin.promptTemplates.profiles.${promptProfileKey(item.profile)}`) }}</div>
                  <div class="font-mono text-xs text-gray-400 dark:text-dark-500">{{ item.profile }}</div>
                </td>
                <td class="px-3 py-2 align-top font-mono text-xs text-gray-700 dark:text-dark-200">
                  {{ item.field || t('admin.promptTemplates.events.notRecorded') }}
                </td>
                <td class="px-3 py-2 align-top">
                  <span :class="['badge', item.supported ? 'badge-success' : 'badge-gray']">
                    {{ item.supported ? t('admin.promptTemplates.preview.supported') : t('admin.promptTemplates.preview.unsupported') }}
                  </span>
                </td>
                <td class="max-w-sm px-3 py-2 align-top">
                  <pre class="max-h-40 overflow-auto whitespace-pre-wrap break-words font-mono text-xs text-gray-600 dark:text-dark-300">{{ displayValue(item.original_value) }}</pre>
                </td>
                <td class="max-w-sm px-3 py-2 align-top">
                  <pre class="max-h-40 overflow-auto whitespace-pre-wrap break-words font-mono text-xs text-gray-900 dark:text-white">{{ displayValue(item.injected_value) }}</pre>
                </td>
              </tr>
              <tr v-if="!result.profile_previews?.length">
                <td colspan="5" class="px-3 py-6 text-center text-sm text-gray-500 dark:text-dark-400">
                  {{ t('admin.promptTemplates.events.notRecorded') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <ul v-if="profileErrors.length" class="list-disc space-y-1 rounded-lg border border-red-200 bg-red-50/60 py-2 pl-8 pr-4 text-xs text-red-700 dark:border-red-900/70 dark:bg-red-950/20 dark:text-red-300">
          <li v-for="(item, index) in profileErrors" :key="index" class="break-words">
            <span class="font-mono">{{ item.profile }}</span>: {{ item.error }}
          </li>
        </ul>

        <p class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.preview.preservedNotice') }}
        </p>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="emit('close')">
          {{ t('admin.promptTemplates.versions.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { promptProfileKey, versionLabel } from '../viewModel'
import { shortenSha } from '../format'
import { previewVersion } from '../api'
import type { PromptPreviewResult, PromptTemplateVersion } from '../types'

const props = defineProps<{
  show: boolean
  version: PromptTemplateVersion | null
}>()

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const result = ref<PromptPreviewResult | null>(null)

const profileErrors = computed(() =>
  (result.value?.profile_previews ?? []).filter((item) => Boolean(item.error)),
)

function displayValue(value: string | undefined): string {
  if (value === undefined || value === null || value === '') {
    return t('admin.promptTemplates.preview.emptyValue')
  }
  return value
}

async function run(versionId: number) {
  loading.value = true
  error.value = ''
  result.value = null
  try {
    result.value = await previewVersion(versionId)
  } catch (caught) {
    error.value = extractI18nErrorMessage(
      caught,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.preview.loadFailed'),
    )
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.show, props.version?.id] as const,
  ([visible, versionId]) => {
    if (!visible || !versionId) {
      result.value = null
      error.value = ''
      return
    }
    void run(versionId)
  },
  { immediate: true },
)
</script>
