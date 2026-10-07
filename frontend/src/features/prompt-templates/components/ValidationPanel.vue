<template>
  <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700 sm:p-5">
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.promptTemplates.validation.title') }}
        </h2>
        <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.validation.description') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="loading"
        data-test="validation-refresh"
        @click="emit('refresh')"
      >
        {{ loading ? t('admin.promptTemplates.validation.validating') : t('admin.promptTemplates.validation.refresh') }}
      </button>
    </header>

    <p v-if="error" role="alert" class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </p>

    <div v-else-if="loading && !report" class="py-6 text-center text-sm text-gray-500 dark:text-dark-400">
      {{ t('admin.promptTemplates.validation.validating') }}
    </div>

    <div v-else-if="report" class="space-y-4">
      <div
        role="status"
        class="flex items-center gap-2 rounded-lg px-4 py-2.5 text-sm font-medium"
        :class="report.valid
          ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300'
          : 'bg-red-50 text-red-700 dark:bg-red-950/30 dark:text-red-300'"
      >
        <Icon :name="report.valid ? 'checkCircle' : 'xCircle'" size="sm" />
        {{ report.valid ? t('admin.promptTemplates.validation.valid') : t('admin.promptTemplates.validation.invalid') }}
      </div>

      <div>
        <p class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">
          {{ t('admin.promptTemplates.validation.errors') }}
        </p>
        <ul
          v-if="errorLabels.length > 0"
          class="list-disc space-y-1 rounded-lg border border-red-200 bg-red-50/60 py-2 pl-8 pr-4 text-sm text-red-700 dark:border-red-900/70 dark:bg-red-950/20 dark:text-red-300"
        >
          <li v-for="(label, index) in errorLabels" :key="index" class="break-words">{{ label }}</li>
        </ul>
        <p v-else class="text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.validation.noErrors') }}
        </p>
      </div>

      <dl class="grid gap-3 text-sm sm:grid-cols-2">
        <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.bodyBytes') }}</dt>
          <dd class="mt-0.5 font-mono text-gray-900 dark:text-white">{{ report.body_bytes }}</dd>
        </div>
        <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.bodySha256') }}</dt>
          <dd class="mt-0.5 break-all font-mono text-xs text-gray-900 dark:text-white" :title="report.body_sha256">
            {{ shortenSha(report.body_sha256) || t('admin.promptTemplates.events.notRecorded') }}
          </dd>
        </div>
        <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.manifestSha256') }}</dt>
          <dd class="mt-0.5 break-all font-mono text-xs text-gray-900 dark:text-white" :title="report.manifest_sha256">
            {{ shortenSha(report.manifest_sha256) || t('admin.promptTemplates.events.notRecorded') }}
          </dd>
        </div>
        <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.supportedProfiles') }}</dt>
          <dd class="mt-0.5 text-gray-900 dark:text-white">
            <template v-if="report.supported_profiles?.length">
              {{ report.supported_profiles.map((profile) => t(`admin.promptTemplates.profiles.${promptProfileKey(profile)}`)).join('、') }}
            </template>
            <span v-else class="text-red-600 dark:text-red-400">{{ t('admin.promptTemplates.events.notRecorded') }}</span>
          </dd>
        </div>
        <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/50 sm:col-span-2">
          <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.clientModels') }}</dt>
          <dd class="mt-0.5 break-words font-mono text-xs text-gray-900 dark:text-white">
            {{ report.client_models?.join(', ') || t('admin.promptTemplates.events.notRecorded') }}
          </dd>
        </div>
      </dl>

      <p v-if="!report.valid" class="text-xs text-amber-700 dark:text-amber-300">
        {{ t('admin.promptTemplates.validation.publishRequiresValid') }}
      </p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { promptProfileKey, validationErrors, validationReasonCode } from '../viewModel'
import { shortenSha } from '../format'
import type { PromptValidationReport } from '../types'

const props = defineProps<{
  report: PromptValidationReport | null
  loading: boolean
  error: string
}>()

const emit = defineEmits<{ refresh: [] }>()

const { t } = useI18n()

/**
 * 后端返回的是原始错误字符串。已知 reason code 翻译成可读文案，
 * 未知 code 或无法解析时原样展示，绝不吞掉信息。
 */
const errorLabels = computed(() =>
  validationErrors(props.report).map((raw) => {
    const code = validationReasonCode(raw)
    if (!code) return raw
    const key = `admin.promptTemplates.validation.reason.${code}`
    const translated = t(key)
    return translated === key ? raw : translated
  }),
)
</script>
