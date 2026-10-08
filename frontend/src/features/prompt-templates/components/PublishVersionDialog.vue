<template>
  <BaseDialog :show="show" :title="t('admin.promptTemplates.versions.publishTitle')" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.promptTemplates.versions.publishDescription') }}
      </p>

      <TextArea
        v-model="changeNote"
        :label="t('admin.promptTemplates.versions.changeNote')"
        :placeholder="t('admin.promptTemplates.versions.changeNotePlaceholder')"
        :rows="3"
        data-test="publish-change-note"
      />

      <p class="text-xs text-gray-500 dark:text-dark-400">
        {{ t('admin.promptTemplates.revisionValue', { revision: draftRevision }) }}
      </p>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="publishing" @click="emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="publishing"
          data-test="publish-confirm"
          @click="confirm"
        >
          {{ publishing ? t('admin.promptTemplates.versions.publishing') : t('admin.promptTemplates.versions.publishConfirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TextArea from '@/components/common/TextArea.vue'
import { newIdempotencyKey } from '../viewModel'

const props = defineProps<{
  show: boolean
  templateId: number
  draftRevision: number
  publishing: boolean
}>()

const emit = defineEmits<{
  close: []
  publish: [payload: { changeNote: string; idempotencyKey: string }]
}>()

const { t } = useI18n()

const changeNote = ref('')
// 每次打开只生成一次幂等键：双击或重试会命中同一个键，后端不会产生第二个版本。
const idempotencyKey = ref('')

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    changeNote.value = ''
    idempotencyKey.value = newIdempotencyKey(props.templateId)
  },
)

function confirm() {
  if (props.publishing) return
  emit('publish', {
    changeNote: changeNote.value.trim(),
    idempotencyKey: idempotencyKey.value || newIdempotencyKey(props.templateId),
  })
}
</script>
