<template>
  <section class="card">
    <header class="card-header flex flex-wrap items-center justify-between gap-3">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.promptTemplates.draft.title') }}
        </h2>
        <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.draft.description') }}
        </p>
      </div>
      <span
        class="whitespace-nowrap rounded-full px-2.5 py-0.5 text-[11px] font-medium"
        :class="dirty
          ? 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
          : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-300'"
        data-test="draft-state"
      >
        {{ dirty ? t('admin.promptTemplates.draft.dirty') : t('admin.promptTemplates.draft.synced') }}
      </span>
    </header>

    <div class="card-body space-y-5">
      <div>
        <div class="mb-1.5 flex flex-wrap items-baseline justify-between gap-2">
          <label class="text-xs font-medium text-gray-700 dark:text-dark-200" for="prompt-draft-body">
            {{ t('admin.promptTemplates.draft.bodyLabel') }}
          </label>
          <span
            class="font-mono text-[11px] tabular-nums"
            :class="overLimit ? 'text-red-600 dark:text-red-400' : 'text-gray-400 dark:text-dark-500'"
          >
            {{ t('admin.promptTemplates.draft.bodyStats', { bytes: bodyBytes, max: PROMPT_BODY_MAX_BYTES, lines: bodyLines }) }}
          </span>
        </div>
        <textarea
          id="prompt-draft-body"
          :value="modelValue.body"
          rows="14"
          spellcheck="false"
          data-test="draft-body"
          class="input w-full resize-y font-mono text-[13px] leading-6"
          :class="overLimit ? 'input-error' : ''"
          :placeholder="t('admin.promptTemplates.draft.bodyPlaceholder')"
          @input="patch({ body: ($event.target as HTMLTextAreaElement).value })"
        ></textarea>
        <p v-if="overLimit" class="mt-1.5 text-[11px] text-red-600 dark:text-red-400">
          {{ t('admin.promptTemplates.draft.bodyBytesOverLimit', { max: PROMPT_BODY_MAX_BYTES }) }}
        </p>
        <p v-else-if="isPlaceholderBody" class="mt-1.5 text-[11px] text-amber-600 dark:text-amber-400">
          {{ t('admin.promptTemplates.draft.placeholderNotice') }}
        </p>
        <p v-else class="mt-1.5 text-[11px] text-gray-400 dark:text-dark-500">
          {{ t('admin.promptTemplates.draft.immutableNotice') }}
        </p>
      </div>

      <div class="grid gap-4 border-t border-gray-100 pt-4 dark:border-dark-700 lg:grid-cols-2">
        <div>
          <label class="mb-1.5 block text-xs font-medium text-gray-700 dark:text-dark-200" for="prompt-draft-client-models">
            {{ t('admin.promptTemplates.draft.clientModels') }}
          </label>
          <textarea
            id="prompt-draft-client-models"
            :value="modelValue.clientModelsText"
            rows="3"
            spellcheck="false"
            data-test="draft-client-models"
            class="input w-full resize-y font-mono text-xs leading-5"
            @input="patch({ clientModelsText: ($event.target as HTMLTextAreaElement).value })"
          ></textarea>
          <p class="mt-1 text-[11px] leading-relaxed text-gray-400 dark:text-dark-500">
            {{ t('admin.promptTemplates.draft.clientModelsHint') }}
          </p>
        </div>

        <div>
          <label class="mb-1.5 block text-xs font-medium text-gray-700 dark:text-dark-200" for="prompt-draft-upstream-models">
            {{ t('admin.promptTemplates.draft.upstreamModels') }}
            <span class="ml-1 font-normal text-gray-400 dark:text-dark-500">{{ t('admin.promptTemplates.draft.upstreamModelsOptional') }}</span>
          </label>
          <textarea
            id="prompt-draft-upstream-models"
            :value="modelValue.upstreamModelsText"
            rows="3"
            spellcheck="false"
            data-test="draft-upstream-models"
            class="input w-full resize-y font-mono text-xs leading-5"
            @input="patch({ upstreamModelsText: ($event.target as HTMLTextAreaElement).value })"
          ></textarea>
          <p class="mt-1 text-[11px] leading-relaxed text-gray-400 dark:text-dark-500">
            {{ t('admin.promptTemplates.draft.upstreamModelsHint') }}
          </p>
        </div>
      </div>

      <div>
        <p class="mb-2 text-xs font-medium text-gray-700 dark:text-dark-200">
          {{ t('admin.promptTemplates.draft.supportedProfiles') }}
        </p>
        <div class="flex flex-wrap gap-2">
          <label
            v-for="profile in PROMPT_PROFILES"
            :key="profile"
            class="inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs transition-colors"
            :class="[
              archived ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
              modelValue.supportedProfiles.includes(profile)
                ? 'border-primary-300 bg-primary-50 text-primary-700 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-300'
                : 'border-gray-200 text-gray-600 hover:bg-gray-50 dark:border-dark-600 dark:text-dark-300 dark:hover:bg-dark-700/60',
            ]"
            :title="profile"
          >
            <input
              type="checkbox"
              class="sr-only"
              :checked="modelValue.supportedProfiles.includes(profile)"
              :disabled="archived"
              :data-test="`draft-profile-${profile}`"
              @change="toggleProfile(profile, ($event.target as HTMLInputElement).checked)"
            />
            <span>{{ t(`admin.promptTemplates.profiles.${promptProfileKey(profile)}`) }}</span>
          </label>
        </div>
        <p class="mt-1.5 text-[11px] leading-relaxed text-gray-400 dark:text-dark-500">
          {{ t('admin.promptTemplates.draft.supportedProfilesHint') }}
        </p>
        <p class="mt-1 text-[11px] leading-relaxed text-gray-400 dark:text-dark-500">
          {{ t('admin.promptTemplates.draft.emptyScopeNotice') }}
        </p>
      </div>
    </div>

    <footer class="card-footer flex flex-wrap items-center justify-end gap-2">
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="!dirty || saving"
        data-test="draft-reset"
        @click="emit('reset')"
      >
        {{ t('admin.promptTemplates.draft.reload') }}
      </button>
      <button
        type="button"
        class="btn btn-primary btn-sm"
        :disabled="!dirty || saving || archived"
        data-test="draft-save"
        @click="emit('save')"
      >
        {{ saving ? t('admin.promptTemplates.draft.saving') : t('admin.promptTemplates.draft.save') }}
      </button>
    </footer>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  PROMPT_BODY_MAX_BYTES,
  PROMPT_PROFILES,
  promptProfileKey,
  utf8ByteLength,
} from '../viewModel'
import type { PromptDraftForm } from '../draftForm'

const props = defineProps<{
  modelValue: PromptDraftForm
  saving: boolean
  archived: boolean
  dirty: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: PromptDraftForm]
  save: []
  reset: []
}>()

const { t } = useI18n()

/** 后端新建模板时写入的占位正文（见 service 层 Body: "placeholder"）。 */
const PLACEHOLDER_BODY = 'placeholder'

const bodyBytes = computed(() => utf8ByteLength(props.modelValue.body))
const bodyLines = computed(() => props.modelValue.body.split('\n').length)
const overLimit = computed(() => bodyBytes.value > PROMPT_BODY_MAX_BYTES)
const isPlaceholderBody = computed(() => props.modelValue.body.trim() === PLACEHOLDER_BODY)

function patch(partial: Partial<PromptDraftForm>) {
  emit('update:modelValue', { ...props.modelValue, ...partial })
}

function toggleProfile(profile: string, checked: boolean) {
  const next = new Set(props.modelValue.supportedProfiles)
  if (checked) next.add(profile)
  else next.delete(profile)
  patch({ supportedProfiles: [...next].sort() })
}
</script>
