<template>
  <form class="grid gap-3 md:grid-cols-2 lg:grid-cols-6" @submit.prevent="submit">
    <div class="lg:col-span-2">
      <Input
        v-model="draft.requestId"
        :label="t('admin.promptTemplates.events.filters.requestId')"
        :placeholder="t('admin.promptTemplates.events.filters.requestIdPlaceholder')"
        data-test="event-filter-request-id"
      />
    </div>
    <Input
      v-model="draft.groupId"
      :label="t('admin.promptTemplates.events.filters.groupId')"
      :placeholder="t('admin.promptTemplates.events.filters.groupIdPlaceholder')"
      data-test="event-filter-group-id"
    />
    <Input
      v-model="draft.versionId"
      :label="t('admin.promptTemplates.events.filters.versionId')"
      :placeholder="t('admin.promptTemplates.events.filters.versionIdPlaceholder')"
      data-test="event-filter-version-id"
    />
    <div>
      <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="event-filter-applied">
        {{ t('admin.promptTemplates.events.filters.applied') }}
      </label>
      <select id="event-filter-applied" v-model="draft.applied" class="input w-full" data-test="event-filter-applied">
        <option value="all">{{ t('admin.promptTemplates.events.filters.appliedAll') }}</option>
        <option value="true">{{ t('admin.promptTemplates.events.filters.appliedOnly') }}</option>
        <option value="false">{{ t('admin.promptTemplates.events.filters.notAppliedOnly') }}</option>
      </select>
    </div>

    <div class="flex items-end gap-3 md:col-span-2 lg:col-span-6">
      <button type="submit" class="btn btn-primary" :disabled="loading" data-test="event-filter-search">
        {{ loading ? t('admin.promptTemplates.events.filters.searching') : t('admin.promptTemplates.events.filters.search') }}
      </button>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="reset">
        {{ t('admin.promptTemplates.events.filters.reset') }}
      </button>
    </div>
  </form>
</template>

<script setup lang="ts">
import { reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import Input from '@/components/common/Input.vue'
import type { PromptRequestEventQuery } from '../types'

defineProps<{ loading: boolean }>()

const emit = defineEmits<{ search: [query: PromptRequestEventQuery] }>()

const { t } = useI18n()

interface FilterDraft {
  requestId: string
  groupId: string
  versionId: string
  applied: 'all' | 'true' | 'false'
}

function emptyDraft(): FilterDraft {
  return { requestId: '', groupId: '', versionId: '', applied: 'all' }
}

const draft = reactive<FilterDraft>(emptyDraft())

function toPositiveInt(value: string): number | undefined {
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function submit() {
  emit('search', {
    request_id: draft.requestId.trim() || undefined,
    group_id: toPositiveInt(draft.groupId),
    version_id: toPositiveInt(draft.versionId),
    applied: draft.applied === 'all' ? undefined : draft.applied === 'true',
  })
}

function reset() {
  Object.assign(draft, emptyDraft())
  submit()
}
</script>
