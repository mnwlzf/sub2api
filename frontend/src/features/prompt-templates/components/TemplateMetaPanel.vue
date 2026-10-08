<template>
  <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700 sm:p-5">
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.promptTemplates.meta.title') }}
        </h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.meta.description') }}
        </p>
      </div>
      <span class="whitespace-nowrap text-xs text-gray-500 dark:text-dark-400">
        {{ t('admin.promptTemplates.revisionValue', { revision: template.revision }) }}
      </span>
    </header>

    <div
      v-if="archived"
      role="status"
      class="mb-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
    >
      <p class="font-medium">{{ t('admin.promptTemplates.meta.archivedNotice') }}</p>
      <p class="mt-1">{{ t('admin.promptTemplates.meta.archivedNoticeDescription') }}</p>
      <p v-if="template.archived_at" class="mt-1 text-xs">
        {{ t('admin.promptTemplates.meta.archivedAt', { time: archivedAtLabel }) }}
      </p>
    </div>

    <div class="grid gap-4 md:grid-cols-2">
      <Input
        v-model="form.name"
        :label="t('admin.promptTemplates.meta.name')"
        :disabled="archived"
        required
      />
      <Input
        v-model="form.source_url"
        :label="t('admin.promptTemplates.meta.sourceUrl')"
        :disabled="archived"
      />
      <div class="md:col-span-2">
        <TextArea
          v-model="form.description"
          :label="t('admin.promptTemplates.meta.descriptionLabel')"
          :rows="2"
          :disabled="archived"
        />
      </div>
      <div class="md:col-span-2">
        <TextArea
          v-model="form.source_note"
          :label="t('admin.promptTemplates.meta.sourceNote')"
          :rows="2"
          :disabled="archived"
        />
      </div>
    </div>

    <div class="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
      <p class="text-xs text-gray-500 dark:text-dark-400">
        {{ dirty ? t('admin.promptTemplates.draft.dirty') : t('admin.promptTemplates.draft.synced') }}
      </p>
      <div class="flex items-center gap-3">
        <button
          v-if="!archived"
          type="button"
          class="btn btn-secondary"
          :disabled="!dirty || saving"
          data-test="save-template-meta"
          @click="submit"
        >
          {{ saving ? t('admin.promptTemplates.meta.saving') : t('admin.promptTemplates.meta.save') }}
        </button>
        <button
          v-if="!archived"
          type="button"
          class="btn btn-secondary text-red-600 dark:text-red-400"
          :disabled="saving"
          data-test="archive-template"
          @click="emit('archive')"
        >
          {{ t('admin.promptTemplates.meta.archive') }}
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Input from '@/components/common/Input.vue'
import TextArea from '@/components/common/TextArea.vue'
import type { PromptTemplate, UpdatePromptTemplatePayload } from '../types'
import { formatDateTime } from '../format'

const props = defineProps<{
  template: PromptTemplate
  saving: boolean
}>()

const emit = defineEmits<{
  save: [payload: UpdatePromptTemplatePayload]
  archive: []
}>()

const { t, locale } = useI18n()

const form = reactive({
  name: '',
  description: '',
  source_url: '',
  source_note: '',
})

const archived = computed(() => Boolean(props.template.archived_at))
const archivedAtLabel = computed(() => formatDateTime(props.template.archived_at, locale.value))

function syncFromTemplate() {
  form.name = props.template.name ?? ''
  form.description = props.template.description ?? ''
  form.source_url = props.template.source_url ?? ''
  form.source_note = props.template.source_note ?? ''
}

watch(
  () => [props.template.id, props.template.revision, props.template.archived_at],
  syncFromTemplate,
  { immediate: true },
)

const dirty = computed(
  () =>
    form.name !== (props.template.name ?? '') ||
    form.description !== (props.template.description ?? '') ||
    form.source_url !== (props.template.source_url ?? '') ||
    form.source_note !== (props.template.source_note ?? ''),
)

function submit() {
  emit('save', {
    name: form.name.trim(),
    description: form.description.trim() || undefined,
    source_url: form.source_url.trim() || undefined,
    source_note: form.source_note.trim() || undefined,
    revision: props.template.revision,
  })
}
</script>
