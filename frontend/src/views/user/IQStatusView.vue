<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl space-y-6 p-4 sm:p-6">
      <!-- Header -->
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-gray-100">
            {{ t('nav.iqStatus') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.pelicanProbe.description') }}
          </p>
        </div>
        <div class="flex items-center gap-2">
          <Select v-model="groupFilter" :options="groupFilterOptions" class="w-52" />
          <button @click="load" class="btn btn-secondary flex items-center gap-1.5 text-sm">
            <Icon name="refresh" size="sm" :stroke-width="2" />
            {{ t('admin.pelicanProbe.refresh') }}
          </button>
        </div>
      </div>

      <div v-if="loading" class="py-10 text-center text-sm text-gray-500">…</div>

      <!-- Disabled -->
      <div
        v-else-if="!status?.enabled"
        class="rounded-xl border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400"
      >
        {{ t('admin.pelicanProbe.probeDisabled') }}
      </div>

      <!-- Empty -->
      <div
        v-else-if="sections.length === 0"
        class="rounded-xl border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400"
      >
        {{ t('admin.pelicanProbe.noPublicPlans') }}
      </div>

      <!-- Grouped status cards -->
      <div v-else class="space-y-8">
        <section v-for="group in sections" :key="group.id" class="space-y-3">
          <div class="flex items-center gap-2">
            <h2 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
              {{ group.name }}
            </h2>
            <span
              class="rounded-full bg-gray-100 px-2 py-0.5 text-[11px] font-medium text-gray-500 dark:bg-gray-700 dark:text-gray-300"
            >
              {{ group.cards.length }}
            </span>
          </div>

          <div v-for="card in group.cards" :key="card.plan.id" class="space-y-2">
            <!-- Status card -->
            <article
              class="overflow-hidden rounded-xl border border-l-4 border-gray-200 bg-white shadow-sm dark:border-gray-700 dark:bg-gray-800"
              :class="accentClass[card.view.state]"
            >
              <div class="flex flex-col gap-4 p-4 sm:flex-row sm:items-center">
                <!-- Left: large status badge -->
                <div
                  class="flex shrink-0 items-center gap-2 rounded-lg px-3 py-2 sm:w-28 sm:flex-col sm:justify-center sm:gap-1.5 sm:self-stretch sm:py-4"
                  :class="badgeClass[card.view.state]"
                >
                  <Icon :name="iconByState[card.view.state]" size="xl" :stroke-width="1.75" class="shrink-0" />
                  <span class="text-xs font-semibold leading-tight sm:text-center">
                    {{ card.view.label }}
                  </span>
                </div>

                <!-- Middle: group · model + latest probe meta -->
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-semibold text-gray-900 dark:text-gray-100">
                    {{ group.name }}<span class="mx-1 text-gray-300 dark:text-gray-600">·</span><span
                      class="font-mono text-xs font-normal text-gray-600 dark:text-gray-300"
                    >{{ card.plan.model_id }}</span>
                  </div>
                  <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                    <span class="inline-flex items-center gap-1">
                      <Icon name="clock" size="xs" />
                      {{ card.latest ? formatTime(card.latest.created_at) : '—' }}
                    </span>
                    <template v-if="card.latest">
                      <span aria-hidden="true">·</span>
                      <span>{{ t('admin.pelicanProbe.latency') }} {{ formatLatency(card.latest.latency_ms) }}</span>
                    </template>
                  </div>
                </div>

                <!-- Right: preview thumbnail, or a compact friendly line on failure -->
                <div class="w-full shrink-0 sm:w-64">
                  <!-- candy answers are prose, so they render as text (never an iframe) -->
                  <ProbeResultBody
                    v-if="card.view.hasPreview && card.view.isCandy"
                    :result="card.latest!"
                    test-type="candy"
                    variant="user"
                    :show-verdict="false"
                    preview-class="h-[180px]"
                  />
                  <iframe
                    v-else-if="card.view.hasPreview"
                    sandbox="allow-scripts"
                    :srcdoc="card.view.html"
                    class="h-[180px] w-full rounded-lg border border-gray-200 bg-white dark:border-gray-700"
                    loading="lazy"
                    title="pelican probe preview"
                  />
                  <div
                    v-else
                    class="flex items-start gap-2 rounded-lg bg-gray-50 px-3 py-2 text-xs leading-relaxed text-gray-600 dark:bg-gray-900/60 dark:text-gray-300"
                  >
                    <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0 text-gray-400" />
                    <span>{{ card.view.message }}</span>
                  </div>
                </div>
              </div>
            </article>

            <!-- History toggle -->
            <div v-if="card.history.length > 0">
              <button class="btn btn-secondary px-3 py-1.5 text-xs" @click="toggleHistory(card.plan.id)">
                {{
                  expandedHistory[card.plan.id]
                    ? t('admin.pelicanProbe.hideHistory')
                    : t('admin.pelicanProbe.viewHistory', { n: card.history.length })
                }}
              </button>
            </div>

            <!-- History (compact cards) -->
            <div
              v-if="expandedHistory[card.plan.id]"
              class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3"
            >
              <div
                v-for="item in card.history"
                :key="item.result.id"
                class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800"
              >
                <div class="flex items-center justify-between gap-2 border-b border-gray-100 px-3 py-1.5 dark:border-gray-700/60">
                  <span class="rounded-full px-2 py-0.5 text-[11px] font-medium" :class="badgeClass[item.view.state]">
                    {{ item.view.label }}
                  </span>
                  <span class="text-[11px] text-gray-400">{{ formatTime(item.result.created_at) }}</span>
                </div>
                <ProbeResultBody
                  v-if="item.view.hasPreview && item.view.isCandy"
                  :result="item.result"
                  test-type="candy"
                  variant="user"
                  :show-verdict="false"
                  preview-class="max-h-32"
                />
                <iframe
                  v-else-if="item.view.hasPreview"
                  sandbox="allow-scripts"
                  :srcdoc="item.view.html"
                  class="h-32 w-full bg-white"
                  loading="lazy"
                  title="pelican probe history preview"
                />
                <div
                  v-else
                  class="flex items-start gap-2 px-3 py-2 text-[11px] leading-relaxed text-gray-500 dark:text-gray-400"
                >
                  <Icon name="infoCircle" size="xs" class="mt-0.5 shrink-0 text-gray-400" />
                  <span>{{ item.view.message }}</span>
                </div>
                <div class="border-t border-gray-100 px-3 py-1.5 text-[11px] text-gray-400 dark:border-gray-700/60">
                  {{ t('admin.pelicanProbe.latency') }} {{ formatLatency(item.result.latency_ms) }}
                </div>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import ProbeResultBody from '@/components/pelican/ProbeResultBody.vue'
import { Icon } from '@/components/icons'
import { getPublicStatus } from '@/api/pelicanProbe'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { PelicanProbePlan, PelicanProbePublicStatus, PelicanProbeResult } from '@/types'

/**
 * User-visible probe state. `nodata` collapses "probe failed" and "no result
 * yet": both mean the user has nothing meaningful to look at, and neither may
 * leak the underlying technical error.
 */
type StatusState = 'pass' | 'degraded' | 'pending' | 'nodata'
type StatusIcon = 'checkCircle' | 'exclamationTriangle' | 'clock' | 'questionCircle'

const iconByState: Record<StatusState, StatusIcon> = {
  pass: 'checkCircle',
  degraded: 'exclamationTriangle',
  pending: 'clock',
  nodata: 'questionCircle'
}

/** Soft block behind the big badge on the left of each card. */
const badgeClass: Record<StatusState, string> = {
  pass: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-400',
  degraded: 'bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-400',
  pending: 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400',
  nodata: 'bg-gray-100 text-gray-600 dark:bg-gray-700/50 dark:text-gray-300'
}

/** Left accent border so a group of cards is scannable at a glance. */
const accentClass: Record<StatusState, string> = {
  pass: 'border-l-emerald-500',
  degraded: 'border-l-rose-500',
  pending: 'border-l-amber-500',
  nodata: 'border-l-gray-300 dark:border-l-gray-600'
}

interface ResultView {
  state: StatusState
  label: string
  message: string
  html: string
  hasPreview: boolean
  /** Candy plans answer in prose: render text instead of an iframe preview. */
  isCandy: boolean
}

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const status = ref<PelicanProbePublicStatus | null>(null)
const groupFilter = ref<number | null>(null)

const groupFilterOptions = computed<SelectOption[]>(() => {
  const seen = new Map<number, string>()
  for (const p of status.value?.plans ?? []) {
    if (!seen.has(p.group_id)) seen.set(p.group_id, p.group_name || `#${p.group_id}`)
  }
  return [
    { value: null, label: t('admin.pelicanProbe.allGroups') },
    ...[...seen.entries()].map(([id, name]) => ({ value: id, label: name }))
  ]
})

const filteredPlans = computed(() => {
  const plans = status.value?.plans ?? []
  if (groupFilter.value == null) return plans
  return plans.filter((p) => p.group_id === groupFilter.value)
})

const resultsByPlan = (planId: number): PelicanProbeResult[] =>
  (status.value?.results ?? [])
    .filter((r) => r.plan_id === planId)
    .slice()
    .sort((a, b) => +new Date(b.created_at) - +new Date(a.created_at))

const formatTime = (v: string | null) => (v ? formatDateTime(v) : '—')

const formatLatency = (ms: number): string =>
  Number.isFinite(ms) && ms > 0 ? `${(ms / 1000).toFixed(1)}s` : '—'

const stateOf = (r?: PelicanProbeResult): StatusState => {
  // A failed run has no usable output at all, so it must not inherit a stale
  // verdict — it reads as "no valid result" (grey) instead of red/green.
  if (!r || r.status === 'failed') return 'nodata'
  if (r.verdict === 'pass') return 'pass'
  if (r.verdict === 'degraded') return 'degraded'
  return 'pending'
}

const stateLabel = (state: StatusState): string => {
  switch (state) {
    case 'pass':
      return t('admin.pelicanProbe.statusNormal')
    case 'degraded':
      return t('admin.pelicanProbe.statusDegraded')
    case 'pending':
      return t('admin.pelicanProbe.statusPending')
    default:
      return t('admin.pelicanProbe.statusNoData')
  }
}

/**
 * Candy plans have an objective answer (21), so their badge names the judge
 * outcome instead of the generic pelican wording. A failed run has no judge
 * outcome at all and keeps the generic "no valid result" label.
 */
const candyStateLabel = (state: StatusState): string => {
  switch (state) {
    case 'pass':
      return t('admin.pelicanProbe.candyVerdictPass')
    case 'degraded':
      return t('admin.pelicanProbe.candyVerdictDegraded')
    case 'pending':
      return t('admin.pelicanProbe.candyVerdictPending')
    default:
      return t('admin.pelicanProbe.statusNoData')
  }
}

/**
 * Plain-language failure message for the user-facing page.
 *
 * `error_message` is deliberately never rendered here: it carries internal
 * gateway URLs and transport details. The backend classifies failures into
 * `failure_reason`; older backends omit the field entirely, in which case every
 * failure degrades to one generic sentence.
 */
const failureText = (r?: PelicanProbeResult): string => {
  if (!r) return t('admin.pelicanProbe.noResults')
  switch (r.failure_reason) {
    case 'upstream':
      return t('admin.pelicanProbe.failureUpstream')
    case 'timeout':
      return t('admin.pelicanProbe.failureTimeout')
    case 'concurrency':
      return t('admin.pelicanProbe.failureConcurrency')
    case 'config':
      return t('admin.pelicanProbe.failureConfig')
    default:
      return t('admin.pelicanProbe.failureUnknown')
  }
}

const viewOf = (r: PelicanProbeResult | undefined, testType: string): ResultView => {
  const state = stateOf(r)
  const html = r && r.status !== 'failed' ? r.raw_html ?? '' : ''
  const isCandy = (testType || '').toLowerCase() === 'candy'
  return {
    state,
    label: isCandy ? candyStateLabel(state) : stateLabel(state),
    message: failureText(r),
    html,
    hasPreview: html.trim() !== '',
    isCandy
  }
}

/** Plans grouped by `group_id`, one section per group, insertion order kept. */
const sections = computed(() => {
  const groups = new Map<number, { id: number; name: string; plans: PelicanProbePlan[] }>()
  for (const plan of filteredPlans.value) {
    const existing = groups.get(plan.group_id)
    if (existing) {
      existing.plans.push(plan)
      continue
    }
    groups.set(plan.group_id, {
      id: plan.group_id,
      name: plan.group_name || `#${plan.group_id}`,
      plans: [plan]
    })
  }
  return [...groups.values()].map((group) => ({
    id: group.id,
    name: group.name,
    cards: group.plans.map((plan) => {
      const results = resultsByPlan(plan.id)
      const latest = results[0]
      const testType = plan.test_type || ''
      return {
        plan,
        latest,
        history: results.slice(1).map((result) => ({ result, view: viewOf(result, testType) })),
        view: viewOf(latest, testType)
      }
    })
  }))
})

const expandedHistory = reactive<Record<number, boolean>>({})
const toggleHistory = (planId: number) => {
  expandedHistory[planId] = !expandedHistory[planId]
}

const load = async () => {
  loading.value = true
  try {
    status.value = await getPublicStatus()
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
