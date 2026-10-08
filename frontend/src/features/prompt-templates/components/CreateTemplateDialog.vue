<template>
  <BaseDialog :show="show" :title="t('admin.promptTemplates.create.title')" width="normal" @close="handleClose">
    <form class="space-y-4" @submit.prevent="submit">
      <Input
        v-model="form.name"
        :label="t('admin.promptTemplates.create.name')"
        :placeholder="t('admin.promptTemplates.create.namePlaceholder')"
        :error="nameError"
        required
        data-test="create-template-name"
      />

      <TextArea
        v-model="form.description"
        :label="t('admin.promptTemplates.create.description')"
        :placeholder="t('admin.promptTemplates.create.descriptionPlaceholder')"
        :rows="2"
      />

      <Input
        v-model="form.source_url"
        :label="t('admin.promptTemplates.create.sourceUrl')"
        :placeholder="t('admin.promptTemplates.create.sourceUrlPlaceholder')"
        :hint="t('admin.promptTemplates.create.hint')"
      />

      <TextArea
        v-model="form.source_note"
        :label="t('admin.promptTemplates.create.sourceNote')"
        :placeholder="t('admin.promptTemplates.create.sourceNotePlaceholder')"
        :rows="2"
      />

      <p v-if="submitError" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
        {{ submitError }}
      </p>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="submitting" @click="handleClose">
          {{ t('admin.promptTemplates.create.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="submitting" data-test="create-template-submit" @click="submit">
          {{ submitting ? t('admin.promptTemplates.meta.saving') : t('admin.promptTemplates.create.submit') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Input from '@/components/common/Input.vue'
import TextArea from '@/components/common/TextArea.vue'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { createTemplate } from '../api'
import type { CreatePromptTemplatePayload, PromptTemplate } from '../types'

const props = defineProps<{ show: boolean }>()

const emit = defineEmits<{
  close: []
  created: [template: PromptTemplate]
}>()

const { t } = useI18n()

const form = reactive<CreatePromptTemplatePayload>({
  name: '',
  description: '',
  source_url: '',
  source_note: '',
})
const submitting = ref(false)
const submitError = ref('')
const nameError = ref('')

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    form.name = ''
    form.description = ''
    form.source_url = ''
    form.source_note = ''
    submitError.value = ''
    nameError.value = ''
    submitting.value = false
  },
)

async function submit() {
  if (submitting.value) return
  nameError.value = ''
  submitError.value = ''
  if (!form.name.trim()) {
    nameError.value = t('admin.promptTemplates.create.nameRequired')
    return
  }
  submitting.value = true
  try {
    const created = await createTemplate({
      name: form.name.trim(),
      description: form.description?.trim() || undefined,
      source_url: form.source_url?.trim() || undefined,
      source_note: form.source_note?.trim() || undefined,
    })
    emit('created', created)
  } catch (error) {
    submitError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.create.failed'),
    )
  } finally {
    submitting.value = false
  }
}

function handleClose() {
  emit('close')
}
</script>
