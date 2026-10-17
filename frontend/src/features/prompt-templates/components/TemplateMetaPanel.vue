<template>
  <section class="card">
    <header class="card-header flex items-center justify-between gap-2">
      <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.promptTemplates.meta.title') }}
      </h2>
      <span
        class="rounded-full bg-gray-100 px-2 py-0.5 text-[11px] font-medium text-gray-500 dark:bg-dark-700 dark:text-dark-300"
        data-test="meta-revision"
      >
        {{ t('admin.promptTemplates.revisionValue', { revision: template.revision }) }}
      </span>
    </header>

    <div class="card-body space-y-3.5">
      <div
        v-if="archived"
        role="status"
        class="rounded-lg bg-amber-50 px-3 py-2 text-[11px] leading-relaxed text-amber-900 dark:bg-amber-950/30 dark:text-amber-200"
      >
        <p class="font-medium">{{ t('admin.promptTemplates.meta.archivedNotice') }}</p>
        <p class="mt-0.5">{{ t('admin.promptTemplates.meta.archiveWarning') }}</p>
        <p v-if="template.archived_at" class="mt-0.5 opacity-80">
          {{ t('admin.promptTemplates.meta.archivedAt', { time: archivedAtLabel }) }}
        </p>
      </div>

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
      <TextArea
        v-model="form.description"
        :label="t('admin.promptTemplates.meta.descriptionLabel')"
        :rows="2"
        :disabled="archived"
      />
      <TextArea
        v-model="form.source_note"
        :label="t('admin.promptTemplates.meta.sourceNote')"
        :rows="2"
        :disabled="archived"
      />
    </div>

    <footer class="card-footer flex flex-wrap items-center justify-between gap-2">
      <span
        class="text-[11px]"
        :class="dirty ? 'text-amber-600 dark:text-amber-400' : 'text-gray-400 dark:text-dark-500'"
      >
        {{ dirty ? t('admin.promptTemplates.draft.dirty') : t('admin.promptTemplates.draft.synced') }}
      </span>
      <div class="flex items-center gap-2">
        <button
          v-if="!archived"
          type="button"
          class="btn btn-ghost btn-sm text-red-600 hover:text-red-700 dark:text-red-400"
          :disabled="saving"
          data-test="archive-template"
          @click="emit('archive')"
        >
          {{ t('admin.promptTemplates.meta.archive') }}
        </button>
        <button
          v-if="!archived"
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="!dirty || saving"
          data-test="save-template-meta"
          @click="submit"
        >
          {{ saving ? t('admin.promptTemplates.meta.saving') : t('admin.promptTemplates.meta.save') }}
        </button>
      </div>
    </footer>
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
