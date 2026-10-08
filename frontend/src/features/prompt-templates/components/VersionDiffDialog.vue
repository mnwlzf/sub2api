<template>
  <BaseDialog
    :show="show"
    :title="previous
      ? t('admin.promptTemplates.versions.diffTitle', { version: versionLabel(previous) })
      : t('admin.promptTemplates.versions.diff')"
    width="wide"
    @close="emit('close')"
  >
    <div v-if="current" class="space-y-4">
      <p
        v-if="!previous"
        role="status"
        class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-2.5 text-sm text-sky-900 dark:border-sky-900/70 dark:bg-sky-950/30 dark:text-sky-200"
      >
        {{ t('admin.promptTemplates.versions.firstVersion') }}
      </p>

      <p
        v-if="previous && !diff.hasChanges"
        role="status"
        class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-600 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-300"
      >
        {{ t('admin.promptTemplates.versions.noDiff') }}
      </p>

      <section v-if="diff.bodyChanged">
        <h3 class="mb-1.5 flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-dark-200">
          {{ t('admin.promptTemplates.versions.diffBody') }}
          <span class="text-xs font-normal text-gray-500 dark:text-dark-400">
            {{ t('admin.promptTemplates.versions.diffBodySummary', { added: diff.addedLines.length, removed: diff.removedLines.length }) }}
          </span>
        </h3>
        <div class="max-h-72 overflow-auto rounded-lg border border-gray-200 dark:border-dark-600">
          <template v-if="diff.removedLines.length">
            <div class="border-b border-gray-200 bg-red-50/60 px-3 py-1.5 text-xs font-medium text-red-700 dark:border-dark-600 dark:bg-red-950/20 dark:text-red-300">
              {{ t('admin.promptTemplates.versions.removed') }}
            </div>
            <pre
              v-for="(line, index) in diff.removedLines"
              :key="`removed-${index}`"
              class="whitespace-pre-wrap break-words px-3 py-0.5 font-mono text-xs text-red-700 dark:text-red-300"
            >- {{ line }}</pre>
          </template>
          <template v-if="diff.addedLines.length">
            <div class="border-b border-t border-gray-200 bg-emerald-50/60 px-3 py-1.5 text-xs font-medium text-emerald-700 dark:border-dark-600 dark:bg-emerald-950/20 dark:text-emerald-300">
              {{ t('admin.promptTemplates.versions.added') }}
            </div>
            <pre
              v-for="(line, index) in diff.addedLines"
              :key="`added-${index}`"
              class="whitespace-pre-wrap break-words px-3 py-0.5 font-mono text-xs text-emerald-700 dark:text-emerald-300"
            >+ {{ line }}</pre>
          </template>
        </div>
      </section>

      <div class="grid gap-4 sm:grid-cols-2">
        <section>
          <h3 class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.promptTemplates.versions.diffModels') }}
          </h3>
          <ul class="space-y-1 text-xs">
            <li v-for="item in diff.clientModelsAdded" :key="`cm-add-${item}`" class="text-emerald-700 dark:text-emerald-300">
              + <span class="font-mono">{{ item }}</span>
            </li>
            <li v-for="item in diff.clientModelsRemoved" :key="`cm-del-${item}`" class="text-red-700 dark:text-red-300">
              − <span class="font-mono">{{ item }}</span>
            </li>
            <li v-if="!diff.clientModelsAdded.length && !diff.clientModelsRemoved.length" class="text-gray-400 dark:text-dark-500">
              {{ t('admin.promptTemplates.events.notRecorded') }}
            </li>
          </ul>
        </section>

        <section>
          <h3 class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.promptTemplates.versions.diffProfiles') }}
          </h3>
          <ul class="space-y-1 text-xs">
            <li v-for="item in diff.profilesAdded" :key="`p-add-${item}`" class="text-emerald-700 dark:text-emerald-300">
              + {{ t(`admin.promptTemplates.profiles.${promptProfileKey(item)}`) }}
            </li>
            <li v-for="item in diff.profilesRemoved" :key="`p-del-${item}`" class="text-red-700 dark:text-red-300">
              − {{ t(`admin.promptTemplates.profiles.${promptProfileKey(item)}`) }}
            </li>
            <li v-if="!diff.profilesAdded.length && !diff.profilesRemoved.length" class="text-gray-400 dark:text-dark-500">
              {{ t('admin.promptTemplates.events.notRecorded') }}
            </li>
          </ul>
        </section>
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
import { diffVersions, promptProfileKey, versionLabel } from '../viewModel'
import type { PromptTemplateVersion } from '../types'

const props = defineProps<{
  show: boolean
  current: PromptTemplateVersion | null
  previous: PromptTemplateVersion | null
}>()

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()

const diff = computed(() =>
  props.current
    ? diffVersions(props.current, props.previous)
    : {
        bodyChanged: false,
        addedLines: [],
        removedLines: [],
        clientModelsAdded: [],
        clientModelsRemoved: [],
        upstreamModelsAdded: [],
        upstreamModelsRemoved: [],
        profilesAdded: [],
        profilesRemoved: [],
        hasChanges: false,
      },
)
</script>
