<template>
  <section class="card">
    <header class="card-header flex items-center justify-between gap-2">
      <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.promptTemplates.validation.title') }}
      </h2>
      <button
        type="button"
        class="text-[11px] font-medium text-gray-500 transition-colors hover:text-primary-600 disabled:opacity-50 dark:text-dark-400 dark:hover:text-primary-400"
        :disabled="loading"
        data-test="validation-refresh"
        @click="emit('refresh')"
      >
        {{ loading ? t('admin.promptTemplates.validation.validating') : t('admin.promptTemplates.validation.refresh') }}
      </button>
    </header>

    <div class="card-body space-y-3">
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-xs text-red-700 dark:bg-red-950/30 dark:text-red-300">
        {{ error }}
      </p>

      <p v-else-if="loading && !report" class="py-4 text-center text-xs text-gray-500 dark:text-dark-400">
        {{ t('admin.promptTemplates.validation.validating') }}
      </p>

      <template v-else-if="report">
        <div
          role="status"
          class="flex items-center gap-2 rounded-lg px-3 py-2 text-xs font-medium"
          :class="report.valid
            ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300'
            : 'bg-red-50 text-red-700 dark:bg-red-950/30 dark:text-red-300'"
        >
          <Icon :name="report.valid ? 'checkCircle' : 'xCircle'" size="sm" />
          {{ report.valid ? t('admin.promptTemplates.validation.valid') : t('admin.promptTemplates.validation.invalid') }}
        </div>

        <ul
          v-if="errorLabels.length > 0"
          class="list-disc space-y-1 rounded-lg bg-red-50/60 py-2 pl-7 pr-3 text-[11px] leading-relaxed text-red-700 dark:bg-red-950/20 dark:text-red-300"
        >
          <li v-for="(label, index) in errorLabels" :key="index" class="break-words">{{ label }}</li>
        </ul>
        <p v-else class="text-[11px] text-gray-400 dark:text-dark-500">
          {{ t('admin.promptTemplates.validation.noErrors') }}
        </p>

        <dl class="grid grid-cols-2 gap-2 text-[11px]">
          <div class="rounded-lg bg-gray-50 px-2.5 py-1.5 dark:bg-dark-700/50">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.bodyBytes') }}</dt>
            <dd class="mt-0.5 font-mono tabular-nums text-gray-900 dark:text-white">{{ report.body_bytes }}</dd>
          </div>
          <div class="rounded-lg bg-gray-50 px-2.5 py-1.5 dark:bg-dark-700/50">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.bodySha256') }}</dt>
            <dd class="mt-0.5 truncate font-mono text-gray-900 dark:text-white" :title="report.body_sha256">
              {{ shortenSha(report.body_sha256) || t('admin.promptTemplates.events.notRecorded') }}
            </dd>
          </div>
          <div class="col-span-2 rounded-lg bg-gray-50 px-2.5 py-1.5 dark:bg-dark-700/50">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.manifestSha256') }}</dt>
            <dd class="mt-0.5 truncate font-mono text-gray-900 dark:text-white" :title="report.manifest_sha256">
              {{ shortenSha(report.manifest_sha256) || t('admin.promptTemplates.events.notRecorded') }}
            </dd>
          </div>
          <div class="col-span-2 rounded-lg bg-gray-50 px-2.5 py-1.5 dark:bg-dark-700/50">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.clientModels') }}</dt>
            <dd class="mt-0.5 break-words font-mono text-gray-900 dark:text-white">
              {{ report.client_models?.join(', ') || t('admin.promptTemplates.events.notRecorded') }}
            </dd>
          </div>
          <div class="col-span-2 rounded-lg bg-gray-50 px-2.5 py-1.5 dark:bg-dark-700/50">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.validation.supportedProfiles') }}</dt>
            <dd class="mt-0.5 text-gray-900 dark:text-white">
              <template v-if="report.supported_profiles?.length">
                {{ report.supported_profiles.map((profile) => t(`admin.promptTemplates.profiles.${promptProfileKey(profile)}`)).join('、') }}
              </template>
              <span v-else class="text-red-600 dark:text-red-400">{{ t('admin.promptTemplates.events.notRecorded') }}</span>
            </dd>
          </div>
        </dl>

        <p v-if="!report.valid" class="text-[11px] text-amber-700 dark:text-amber-300">
          {{ t('admin.promptTemplates.validation.publishRequiresValid') }}
        </p>
      </template>
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
