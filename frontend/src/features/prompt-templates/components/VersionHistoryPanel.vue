<template>
  <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700 sm:p-5">
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.promptTemplates.versions.title') }}
        </h2>
        <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.versions.description') }}
        </p>
      </div>
      <div class="flex items-center gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="emit('refresh')">
          {{ t('admin.promptTemplates.list.refresh') }}
        </button>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="publishing || !canPublish"
          :title="canPublish ? undefined : t('admin.promptTemplates.validation.publishRequiresValid')"
          data-test="open-publish"
          @click="emit('publish')"
        >
          {{ publishing ? t('admin.promptTemplates.versions.publishing') : t('admin.promptTemplates.versions.publish') }}
        </button>
      </div>
    </header>

    <div
      role="status"
      class="mb-4 rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-300"
    >
      {{ t('admin.promptTemplates.versions.immutableNotice') }}
    </div>

    <p v-if="error" role="alert" class="mb-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </p>

    <DataTable
      :columns="columns"
      :data="rows"
      :loading="loading"
      row-key="id"
      :virtualize-threshold="50"
    >
      <template #cell-version="{ row }">
        <span class="font-medium text-gray-900 dark:text-white">{{ versionLabel(row.version) }}</span>
      </template>

      <template #cell-changeNote="{ row }">
        <span class="block max-w-xs truncate text-gray-600 dark:text-dark-300" :title="row.version.change_note">
          {{ row.version.change_note || t('admin.promptTemplates.events.notRecorded') }}
        </span>
      </template>

      <template #cell-manifest="{ row }">
        <span class="font-mono text-xs text-gray-500 dark:text-dark-400" :title="row.version.manifest_sha256">
          {{ shortenSha(row.version.manifest_sha256) }}
        </span>
      </template>

      <template #cell-actions="{ row }">
        <div class="flex items-center justify-end gap-2">
          <button type="button" class="btn btn-secondary btn-sm" @click="emit('viewBody', row.version)">
            {{ t('admin.promptTemplates.versions.viewBody') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :data-test="`version-diff-${row.id}`"
            @click="emit('diff', row.version)"
          >
            {{ t('admin.promptTemplates.versions.diff') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :data-test="`version-preview-${row.id}`"
            @click="emit('preview', row.version)"
          >
            {{ t('admin.promptTemplates.versions.preview') }}
          </button>
        </div>
      </template>

      <template #empty>
        <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.versions.empty') }}</p>
      </template>
    </DataTable>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import { versionLabel } from '../viewModel'
import { formatDateTime, shortenSha } from '../format'
import type { PromptTemplateVersion } from '../types'

interface VersionRow {
  id: number
  version: PromptTemplateVersion
  publishedAt: string
  bodyBytes: number
}

const props = defineProps<{
  versions: PromptTemplateVersion[]
  loading: boolean
  error: string
  canPublish: boolean
  publishing: boolean
}>()

const emit = defineEmits<{
  refresh: []
  publish: []
  preview: [version: PromptTemplateVersion]
  viewBody: [version: PromptTemplateVersion]
  diff: [version: PromptTemplateVersion]
}>()

const { t, locale } = useI18n()

const columns = computed<Column[]>(() => [
  { key: 'version', label: t('admin.promptTemplates.versions.columns.version'), sortable: true },
  { key: 'changeNote', label: t('admin.promptTemplates.versions.columns.changeNote') },
  { key: 'publishedAt', label: t('admin.promptTemplates.versions.columns.publishedAt'), sortable: true },
  { key: 'bodyBytes', label: t('admin.promptTemplates.versions.columns.bodyBytes'), sortable: true },
  { key: 'manifest', label: t('admin.promptTemplates.versions.columns.manifest') },
  { key: 'actions', label: t('admin.promptTemplates.versions.columns.actions'), class: 'text-right' },
])

const rows = computed<VersionRow[]>(() =>
  [...props.versions]
    .sort((a, b) => b.version_no - a.version_no)
    .map((version) => ({
      id: version.id,
      version,
      publishedAt: formatDateTime(version.published_at, locale.value),
      bodyBytes: version.body_bytes,
    })),
)
</script>
