<template>
  <BaseDialog
    :show="show"
    :title="version ? t('admin.promptTemplates.versions.bodyTitle', { version: versionLabel(version) }) : ''"
    width="wide"
    @close="emit('close')"
  >
    <div v-if="version" class="space-y-4">
      <div
        role="status"
        class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-300"
      >
        {{ t('admin.promptTemplates.versions.immutableNotice') }}
      </div>

      <dl class="grid gap-3 text-sm sm:grid-cols-3">
        <div>
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.versions.columns.publishedAt') }}</dt>
          <dd class="mt-0.5 text-gray-900 dark:text-white">{{ publishedAtLabel }}</dd>
        </div>
        <div>
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.versions.columns.bodyBytes') }}</dt>
          <dd class="mt-0.5 font-mono text-gray-900 dark:text-white">{{ version.body_bytes }}</dd>
        </div>
        <div>
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.versions.columns.manifest') }}</dt>
          <dd class="mt-0.5 break-all font-mono text-xs text-gray-900 dark:text-white">{{ version.manifest_sha256 }}</dd>
        </div>
      </dl>

      <div>
        <p class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">
          {{ t('admin.promptTemplates.draft.bodyLabel') }}
        </p>
        <pre
          class="max-h-[420px] overflow-auto whitespace-pre-wrap break-words rounded-lg border border-gray-200 bg-gray-50 p-4 font-mono text-xs text-gray-800 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-100"
        >{{ version.body }}</pre>
      </div>
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
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { versionLabel } from '../viewModel'
import { formatDateTime } from '../format'
import type { PromptTemplateVersion } from '../types'

const props = defineProps<{
  show: boolean
  version: PromptTemplateVersion | null
}>()

const emit = defineEmits<{ close: [] }>()

const { t, locale } = useI18n()

const publishedAtLabel = computed(() => formatDateTime(props.version?.published_at, locale.value))
</script>
