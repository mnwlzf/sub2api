<template>
  <BaseDialog :show="show" :title="dialogTitle" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.promptTemplates.binding.overrides.dialogDescription') }}
      </p>

      <p
        v-if="groupBindingDisabled"
        role="status"
        class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-2.5 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
      >
        {{ t('admin.promptTemplates.binding.overrides.groupDisabledWarning') }}
      </p>

      <div>
        <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="override-account">
          {{ t('admin.promptTemplates.binding.overrides.columns.account') }}
        </label>
        <Select
          id="override-account"
          v-model="accountId"
          :options="accountOptions"
          :placeholder="t('admin.promptTemplates.binding.overrides.accountPlaceholder')"
          :disabled="isEditing"
          searchable
          data-test="override-account-select"
        />
      </div>

      <div>
        <p class="mb-2 text-sm font-medium text-gray-700 dark:text-dark-200">
          {{ t('admin.promptTemplates.binding.overrides.modeLabel') }}
        </p>
        <div class="space-y-2">
          <label
            v-for="option in modeOptions"
            :key="option.value"
            class="flex cursor-pointer items-start gap-2.5 rounded-lg border border-gray-200 px-3 py-2 dark:border-dark-600"
            :class="mode === option.value ? 'bg-primary-50/60 dark:bg-primary-900/10' : 'hover:bg-gray-50 dark:hover:bg-dark-700/60'"
          >
            <input
              type="radio"
              class="mt-0.5 h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800"
              :value="option.value"
              :checked="mode === option.value"
              :data-test="`override-mode-${option.value}`"
              @change="mode = option.value"
            />
            <span>
              <span class="block text-sm text-gray-800 dark:text-dark-100">{{ option.label }}</span>
              <span class="mt-0.5 block text-xs text-gray-500 dark:text-dark-400">{{ option.hint }}</span>
            </span>
          </label>
        </div>
      </div>

      <div v-if="mode === 'version'">
        <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="override-version">
          {{ t('admin.promptTemplates.binding.overrides.selectVersion') }}
        </label>
        <Select
          id="override-version"
          v-model="versionId"
          :options="versionOptions"
          :placeholder="t('admin.promptTemplates.binding.selectVersionPlaceholder')"
          searchable
          data-test="override-version-select"
        />
        <p v-if="mode === 'version' && !versionId" class="mt-1.5 text-xs text-amber-700 dark:text-amber-300">
          {{ t('admin.promptTemplates.binding.overrides.selectVersionFirst') }}
        </p>
      </div>

      <p v-if="submitError" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
        {{ submitError }}
      </p>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">
          {{ t('admin.promptTemplates.binding.overrides.close') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="saving || !accountId || (mode === 'version' && !versionId)"
          data-test="override-save"
          @click="submit"
        >
          {{ saving ? t('admin.promptTemplates.binding.overrides.saving') : t('admin.promptTemplates.binding.overrides.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="showConflict"
    :title="t('admin.promptTemplates.conflict.title')"
    :message="t('admin.promptTemplates.conflict.message')"
    :confirm-text="t('admin.promptTemplates.conflict.reload')"
    :cancel-text="t('admin.promptTemplates.conflict.dismiss')"
    @confirm="handleConflictReload"
    @cancel="showConflict = false"
  />
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import type { SelectOption } from '@/types'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { setAccountOverride } from '@/features/prompt-templates/api'
import type { AccountGroupPromptOverride, PromptOverrideMode } from '@/features/prompt-templates/types'

const props = defineProps<{
  show: boolean
  groupId: number
  accounts: Array<{ id: number; name: string }>
  existing: AccountGroupPromptOverride | null
  versionOptions: SelectOption[]
  groupBindingDisabled: boolean
}>()

const emit = defineEmits<{
  close: []
  saved: [override: AccountGroupPromptOverride]
  reload: []
}>()

const { t } = useI18n()

const accountId = ref<number | null>(null)
const mode = ref<PromptOverrideMode>('inherit')
const versionId = ref<number | null>(null)
const revision = ref(0)
const saving = ref(false)
const submitError = ref('')
const showConflict = ref(false)

const isEditing = computed(() => Boolean(props.existing))

const accountOptions = computed<SelectOption[]>(() =>
  props.accounts.map((account) => ({ value: account.id, label: `${account.name} (#${account.id})` })),
)

const modeOptions = computed<Array<{ value: PromptOverrideMode; label: string; hint: string }>>(() => [
  {
    value: 'inherit',
    label: t('admin.promptTemplates.binding.overrides.modeInherit'),
    hint: t('admin.promptTemplates.binding.overrides.inheritHint'),
  },
  {
    value: 'disabled',
    label: t('admin.promptTemplates.binding.overrides.modeDisabled'),
    hint: t('admin.promptTemplates.binding.overrides.disabledHint'),
  },
  {
    value: 'version',
    label: t('admin.promptTemplates.binding.overrides.modeVersion'),
    hint: t('admin.promptTemplates.binding.overrides.versionHint'),
  },
])

const dialogTitle = computed(() => {
  if (!props.existing) return t('admin.promptTemplates.binding.overrides.addOverride')
  const account = props.accounts.find((item) => item.id === props.existing?.account_id)
  const label = account?.name ?? `#${props.existing.account_id}`
  return t('admin.promptTemplates.binding.overrides.dialogTitle', { account: label })
})

watch(
  () => [props.show, props.existing?.account_id, props.existing?.revision] as const,
  ([visible]) => {
    if (!visible) return
    submitError.value = ''
    showConflict.value = false
    saving.value = false
    if (props.existing) {
      accountId.value = props.existing.account_id
      mode.value = props.existing.mode
      versionId.value = props.existing.version_id ?? null
      revision.value = props.existing.revision
    } else {
      accountId.value = null
      mode.value = 'inherit'
      versionId.value = null
      revision.value = 0
    }
  },
  { immediate: true },
)

function isConflict(error: unknown): boolean {
  return extractApiErrorCode(error) === 'PROMPT_REVISION_CONFLICT' || (error as { status?: number })?.status === 409
}

async function submit() {
  if (!accountId.value || saving.value) return
  saving.value = true
  submitError.value = ''
  try {
    const saved = await setAccountOverride(props.groupId, accountId.value, {
      mode: mode.value,
      version_id: mode.value === 'version' ? versionId.value : null,
      revision: revision.value,
    })
    emit('saved', saved)
  } catch (error) {
    if (isConflict(error)) {
      showConflict.value = true
    } else {
      submitError.value = extractApiErrorMessage(
        error,
        t('admin.promptTemplates.binding.overrides.saveFailed'),
      )
    }
  } finally {
    saving.value = false
  }
}

function handleConflictReload() {
  showConflict.value = false
  emit('reload')
  emit('close')
}
</script>
