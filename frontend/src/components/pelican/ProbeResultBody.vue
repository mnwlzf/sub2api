<template>
  <div class="relative">
    <!-- verdict ribbon: candy gets its own "judged 21 / not judged 21" wording -->
    <div
      v-if="showVerdict"
      class="absolute right-3 top-3 z-10 rounded-full px-2.5 py-0.5 text-xs font-semibold shadow"
      :class="verdictClass"
    >
      {{ verdictLabel }}
    </div>

    <!-- failure -->
    <div
      v-if="isFailed"
      class="flex items-center justify-center bg-gray-50 p-4 dark:bg-gray-900"
      :class="previewClass"
    >
      <p class="break-all text-xs text-rose-600">{{ failureText }}</p>
    </div>

    <!-- candy, user-facing: the verdict IS the whole message. The model's prose
         answer and the judge note are review material and stay admin-side. -->
    <div v-else-if="isCandy && isUser" class="p-3">
      <div class="rounded-lg px-2.5 py-1.5 text-xs font-semibold" :class="candyVerdictClass">
        {{ candyVerdictText }}
      </div>
    </div>

    <!-- candy, admin: the model answered in prose, so render text — never an iframe -->
    <div v-else-if="isCandy" class="space-y-2 p-3">
      <div class="flex items-center justify-between gap-2">
        <span class="text-xs font-medium text-gray-500 dark:text-gray-400">
          {{ t('admin.pelicanProbe.candyAnswerLabel') }}
        </span>
        <!-- Deterministic parse of the "ANSWER: <n>" line; absent when the
             answer ignored the contract (judge fallback / manual review). -->
        <span
          v-if="parsedAnswer !== null"
          class="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-[11px] text-gray-700 dark:bg-gray-700/60 dark:text-gray-200"
        >
          {{ t('admin.pelicanProbe.candyParsedAnswer', { answer: parsedAnswer }) }}
        </span>
      </div>
      <!-- Vue interpolation escapes the answer; raw_html must never be v-html'd. -->
      <pre
        class="overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-2.5 font-mono text-xs leading-relaxed text-gray-800 dark:bg-gray-900 dark:text-gray-200"
        :class="previewClass"
      >{{ result.raw_html }}</pre>
      <div class="rounded-lg px-2.5 py-1.5 text-xs font-semibold" :class="candyVerdictClass">
        {{ candyVerdictText }}
      </div>
    </div>

    <!-- pelican: the model produced an HTML/SVG document, preview it -->
    <iframe
      v-else
      sandbox="allow-scripts"
      :srcdoc="result.raw_html"
      class="w-full bg-white"
      :class="previewClass"
      loading="lazy"
      :title="previewTitle"
    />

    <p
      v-if="displayNote"
      class="mx-3 mb-2 mt-1 rounded bg-gray-50 p-1.5 text-xs text-gray-600 dark:bg-gray-900 dark:text-gray-300"
    >
      {{ displayNote }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PelicanProbeResult } from '@/types'

// ProbeResultBody renders one probe result body. It exists so the admin results
// wall and the user-facing status page share a single branch:
//
//   * candy  → the model produced a prose answer. Admins reviewing the run see
//              the answer (escaped text) and the judge note; end users see the
//              verdict alone, because the answer is review material.
//   * pelican → the model produced HTML/SVG; keep the sandboxed iframe preview.
//   * failed  → admin keeps the raw upstream error, users get a plain sentence
//              (raw errors embed internal endpoints such as
//              `Post "http://127.0.0.1:8080/v1/chat/completions"`).
//
// The `user` variant is deliberately austere: no answer body, no judge note, no
// diagnostics — every one of those leaked internal detail (or model prose) onto
// a page users are meant to read at a glance.

// test_type is not part of the plan-result payload yet; when a backend starts
// sending it, prefer it over the plan lookup.
type ResultWithTestType = PelicanProbeResult & { test_type?: string }

const props = withDefaults(
  defineProps<{
    result: PelicanProbeResult
    /** Plan test type ('candy' | 'pelican'); empty falls back to result.test_type. */
    testType?: string
    /** 'user' hides the answer body, notes and raw upstream errors; 'admin' keeps them. */
    variant?: 'admin' | 'user'
    /** Height/scroll classes for the iframe or the text answer box. */
    previewClass?: string
    /**
     * Whether to draw the verdict ribbon. Views that already render their own
     * status badge (the user-facing status cards) turn it off.
     */
    showVerdict?: boolean
    /** Accessible name of the pelican preview frame. */
    previewTitle?: string
  }>(),
  {
    testType: '',
    variant: 'admin',
    previewClass: 'h-56',
    showVerdict: true,
    previewTitle: 'pelican probe preview'
  }
)

const { t } = useI18n()

const isUser = computed(() => props.variant === 'user')

const isCandy = computed(
  () =>
    ((props.testType || (props.result as ResultWithTestType).test_type || '') as string).toLowerCase() ===
    'candy'
)

const isFailed = computed(() => props.result.status === 'failed')

const failureText = computed(() => {
  if (isUser.value) {
    return t('admin.pelicanProbe.resultUnavailable')
  }
  return props.result.error_message || t('admin.pelicanProbe.statusFailed')
})

// Judge notes double as diagnostics ("auto-judge skipped: judge HTTP 404: …")
// and can embed the internal gateway URL, so the user-facing view drops them
// entirely rather than trying to tell the two kinds apart.
const displayNote = computed(() => {
  if (isUser.value) return ''
  return (props.result.note || '').trim()
})

const verdictClass = computed(() => {
  // A failed run carries no judgement, so it never gets the amber "awaiting
  // review" colour — that badge sits next to pass/degraded buttons and reads as
  // "someone should decide this".
  if (isFailed.value) return 'bg-gray-500 text-white'
  switch (props.result.verdict) {
    case 'pass':
      return 'bg-emerald-600 text-white'
    case 'degraded':
      return 'bg-rose-700 text-white'
    default:
      return 'bg-amber-500 text-white'
  }
})

const verdictLabel = computed(() => {
  // Checked before the verdict: rows written before PelicanVerdictInvalid
  // existed still carry status=failed + verdict=pending, and they are just as
  // unreviewable as new ones.
  if (isFailed.value) return t('admin.pelicanProbe.verdictInvalid')
  if (isCandy.value) return candyVerdictText.value
  switch (props.result.verdict) {
    case 'pass':
      return t('admin.pelicanProbe.verdictPass')
    case 'degraded':
      return t('admin.pelicanProbe.verdictDegraded')
    default:
      return t('admin.pelicanProbe.verdictPending')
  }
})

const candyVerdictText = computed(() => {
  if (isFailed.value) return t('admin.pelicanProbe.candyVerdictInvalid')
  switch (props.result.verdict) {
    case 'pass':
      return t('admin.pelicanProbe.candyVerdictPass')
    case 'degraded':
      return t('admin.pelicanProbe.candyVerdictDegraded')
    default:
      return t('admin.pelicanProbe.candyVerdictPending')
  }
})

// Deterministic parse of the "ANSWER: <n>" line. The backend omits the field
// when the answer ignored the contract, so absence must render nothing rather
// than a zero.
const parsedAnswer = computed(() => {
  const value = props.result.answer_value
  return typeof value === 'number' ? value : null
})

const candyVerdictClass = computed(() => {
  if (isFailed.value) {
    return 'bg-gray-100 text-gray-600 dark:bg-gray-700/50 dark:text-gray-300'
  }
  switch (props.result.verdict) {
    case 'pass':
      return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'degraded':
      return 'bg-rose-50 text-rose-700 dark:bg-rose-900/30 dark:text-rose-300'
    default:
      return 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  }
})
</script>
