<template>
  <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700 sm:p-5">
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.promptTemplates.draft.title') }}
        </h2>
        <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.draft.description') }}
        </p>
      </div>
      <span
        class="whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium"
        :class="dirty
          ? 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
          : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'"
      >
        {{ dirty ? t('admin.promptTemplates.draft.dirty') : t('admin.promptTemplates.draft.synced') }}
      </span>
    </header>

    <div
      role="status"
      class="mb-4 rounded-lg border border-sky-200 bg-sky-50 px-4 py-2.5 text-xs text-sky-900 dark:border-sky-900/70 dark:bg-sky-950/30 dark:text-sky-200"
    >
      {{ t('admin.promptTemplates.draft.immutableNotice') }}
    </div>

    <div
      v-if="isPlaceholderBody"
      role="status"
      class="mb-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-2.5 text-xs text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
    >
      {{ t('admin.promptTemplates.draft.placeholderNotice') }}
    </div>

    <fieldset :disabled="archived" class="space-y-4">
      <div>
        <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="prompt-draft-body">
          {{ t('admin.promptTemplates.draft.bodyLabel') }}
        </label>
        <textarea
          id="prompt-draft-body"
          :value="modelValue.body"
          rows="12"
          spellcheck="false"
          data-test="draft-body"
          class="input w-full font-mono text-sm"
          :placeholder="t('admin.promptTemplates.draft.bodyPlaceholder')"
          @input="patch({ body: ($event.target as HTMLTextAreaElement).value })"
        ></textarea>
        <p
          class="mt-1.5 text-xs"
          :class="overLimit ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'"
        >
          {{ t('admin.promptTemplates.draft.bodyBytes', { bytes: bodyBytes, max: PROMPT_BODY_MAX_BYTES }) }}
          <span v-if="overLimit" class="ml-1">{{ t('admin.promptTemplates.draft.bodyBytesOverLimit', { max: PROMPT_BODY_MAX_BYTES }) }}</span>
        </p>
      </div>

      <div class="grid gap-4 md:grid-cols-2">
        <div>
          <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="prompt-draft-client-models">
            {{ t('admin.promptTemplates.draft.clientModels') }}
          </label>
          <textarea
            id="prompt-draft-client-models"
            :value="modelValue.clientModelsText"
            rows="5"
            spellcheck="false"
            data-test="draft-client-models"
            class="input w-full font-mono text-sm"
            @input="patch({ clientModelsText: ($event.target as HTMLTextAreaElement).value })"
          ></textarea>
          <p class="mt-1.5 text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.promptTemplates.draft.clientModelsHint') }}
          </p>
        </div>

        <div>
          <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="prompt-draft-upstream-models">
            {{ t('admin.promptTemplates.draft.upstreamModels') }}
            <span class="ml-1 font-normal text-gray-400 dark:text-dark-500">({{ t('admin.promptTemplates.draft.upstreamModelsOptional') }})</span>
          </label>
          <textarea
            id="prompt-draft-upstream-models"
            :value="modelValue.upstreamModelsText"
            rows="5"
            spellcheck="false"
            data-test="draft-upstream-models"
            class="input w-full font-mono text-sm"
            @input="patch({ upstreamModelsText: ($event.target as HTMLTextAreaElement).value })"
          ></textarea>
          <p class="mt-1.5 text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.promptTemplates.draft.upstreamModelsHint') }}
          </p>
        </div>
      </div>

      <div>
        <p class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">
          {{ t('admin.promptTemplates.draft.supportedProfiles') }}
        </p>
        <div class="grid gap-2 sm:grid-cols-2">
          <label
            v-for="profile in PROMPT_PROFILES"
            :key="profile"
            class="flex items-start gap-2.5 rounded-lg border border-gray-200 px-3 py-2 text-sm dark:border-dark-600"
            :class="archived ? 'opacity-60' : 'cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-700/60'"
          >
            <input
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800"
              :checked="modelValue.supportedProfiles.includes(profile)"
              :disabled="archived"
              :data-test="`draft-profile-${profile}`"
              @change="toggleProfile(profile, ($event.target as HTMLInputElement).checked)"
            />
            <span class="text-gray-700 dark:text-dark-200">
              {{ t(`admin.promptTemplates.profiles.${promptProfileKey(profile)}`) }}
              <span class="ml-1 font-mono text-xs text-gray-400 dark:text-dark-500">{{ profile }}</span>
            </span>
          </label>
        </div>
        <p class="mt-1.5 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.draft.supportedProfilesHint') }}
        </p>
      </div>
    </fieldset>

    <div
      role="status"
      class="mt-4 rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-300"
    >
      {{ t('admin.promptTemplates.draft.emptyScopeNotice') }}
    </div>

    <div class="mt-4 flex flex-wrap items-center justify-end gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
      <button
        type="button"
        class="btn btn-secondary"
        :disabled="!dirty || saving"
        data-test="draft-reset"
        @click="emit('reset')"
      >
        {{ t('admin.promptTemplates.draft.reload') }}
      </button>
      <button
        type="button"
        class="btn btn-primary"
        :disabled="!dirty || saving || archived"
        data-test="draft-save"
        @click="emit('save')"
      >
        {{ saving ? t('admin.promptTemplates.draft.saving') : t('admin.promptTemplates.draft.save') }}
      </button>
    </div>
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
